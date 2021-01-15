// +build pubnub

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"

	pngo "github.com/pubnub/go"
	"github.com/rs/xid"
)

func init() {
	log.Println("[init] loading the PubNub plugin ...")
	plugins["pubnub"] = new(pubnubPlugin)
}

type pubnubPlugin struct {
	isSetup bool
	client  struct {
		conn    map[string]*pngo.PubNub
		channel map[string]string
	}

	On map[string]func(string, string, interface{})
}

func (p *pubnubPlugin) Setup(config *Config) (err error) {
	log.Println("[pubnub] setup plugin ...")

	p.client.conn = make(map[string]*pngo.PubNub)
	p.client.channel = make(map[string]string)
	p.On = make(map[string]func(string, string, interface{}))

	for _, v := range config.Servers {
		if v.PubNub == nil {
			continue
		}

		if v.PubNub.UUID == "" {
			v.PubNub.UUID = xid.New().String()
		}

		publishKey, err := v.PubNub.PublishKey.Expr.Value(&fileEvalCtx)
		if err != nil {
			return err
		}

		subscribeKey, err := v.PubNub.SubscribeKey.Expr.Value(&fileEvalCtx)
		if err != nil {
			return err
		}

		var conf = pngo.NewConfig()
		conf.PublishKey = publishKey.AsString()
		conf.SubscribeKey = subscribeKey.AsString()
		conf.UUID = v.PubNub.UUID

		p.client.conn[v.PubNub.Name] = pngo.NewPubNub(conf)
		p.client.channel[v.PubNub.Name] = v.PubNub.Channel

		log.Printf("[pubnub] client %s (channel: %q uuid: %q) ...", v.PubNub.Name, v.PubNub.Channel, v.PubNub.UUID)
	}

	listener := p.NewListener()

	for _, ws := range config.Websockets {
		for _, pn := range ws.PubNub {
			p.Subscribe(pn, listener)
		}
	}

	return nil
}

func (p *pubnubPlugin) NewListener() *pngo.Listener {
	log.Println("[pubnub] setup a listener ...")

	var listener = pngo.NewListener()

	go func() {
		for {
			select {
			case message := <-listener.Message:
				var ch, ns, event string
				var data interface{}
				if msg, ok := message.Message.(map[string]interface{}); ok {
					ch = message.Channel
					ns, _ = msg["ns"].(string)
					event, _ = msg["name"].(string)
					data, _ = msg["data"]
				}
				log.Printf("[pubnub] message %s %s %s ...", ch, ns, event)

				key := fmt.Sprintf("%s+%s+%s", ch, ns, event) // just smash together so we can have a unique event
				if key == "++" {
					continue
				}

				if _, ok := p.On[key]; ok {
					log.Println("[pubnub] execute message callback ...")
					p.On[key](message.Publisher, ns, data)
				}
			}
		}
	}()

	return listener
}

func (p *pubnubPlugin) Subscribe(pn pubnub, listener *pngo.Listener) {
	log.Printf("[pubnub] subcribe to %q ...", pn.Name)

	conn, ok := p.client.conn[pn.Name]
	if !ok {
		return
	}

	for _, sio := range pn.SubscribeSocketIO {
		name := p.client.channel[pn.Name]

		conn.Subscribe().Channels([]string{name}).Execute()
		p.client.conn[pn.Name].AddListener(listener)

		// setup the callback
		p.On[fmt.Sprintf("%s+%s+%s", name, sio.Namespace, sio.Event)] = func(sid, ns string, _ interface{}) {
			log.Printf("[pubnub] callback message %s %s %s ...", name, sio.Namespace, sio.Event)

			if len(sio.Emit) > 0 || len(sio.Broadcast) > 0 {
				if len(sio.Delay) > 0 {
					log.Printf("[pubnub] callback delay response for %s ...", sio.Delay)
					time.Sleep(delay(sio.Delay))
				}
			}

			for _, pub := range sio.Emit {
				if pub.Data == nil {
					continue
				}

				dataVal, err := pub.Data.Expr.Value(&bodyEvalCtx)
				if log.OnErr(err).Printf("[pubnub] callback failed to emit: %v", err).HasErr() {
					return
				}

				msg := map[string]interface{}{
					"name": sid,
					"ns":   ns,
					"data": toObject(dataVal.AsString()),
				}

				log.Println("[pubnub] callback emit ...")
				conn.Publish().Channel(pub.Channel).Message(msg).Execute() // TODO(njones):look for errors
			}

			for _, pub := range sio.Broadcast {
				if pub.Data == nil {
					continue
				}

				dataVal, err := pub.Data.Expr.Value(&bodyEvalCtx)
				if log.OnErr(err).Printf("[pubnub] callback failed to broadcast: %v", err).HasErr() {
					return
				}

				msg := map[string]interface{}{
					"name": sid,
					"ns":   ns,
					"data": toObject(dataVal.AsString()),
				}

				log.Println("[pubnub] callback broadcast ...")
				conn.Publish().Channel(pub.Channel).Message(msg).Execute() // TODO(njones):look for errors
			}
		}
	}
}

func (p *pubnubPlugin) Serve(r route, req request) (func(http.Handler) http.Handler, bool) {
	if len(r.PubNub) == 0 {
		return nil, false
	}

	var idx = int64(-1)
	var resps = req.PubNub
	if req.Order == "unordered" {
		rand.Seed(time.Now().UnixNano()) // doesn't have to be crypto-quality random here...
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { next.ServeHTTP(w, r) }()

			timeoutTimer := time.NewTimer(1 * time.Minute) // HARDCODED FOR NOW
			timeout := timeoutTimer.C
			go func() {
				defer timeoutTimer.Stop()

				for {
					var x int64
					switch req.Order {
					case "random":
						x = rand.Int63n(int64(len(resps) * 2))
					case "unordered":
						x = atomic.AddInt64(&idx, 1)
						if int(x)%len(resps) == 0 {
							rand.Shuffle(len(resps), func(i, j int) { resps[i], resps[j] = resps[j], resps[i] })
						}
					default:
						x = atomic.AddInt64(&idx, 1)
					}
					resp := resps[int(x)%len(resps)]
					conn, ok := p.client.conn[resp.Name]
					if !ok {
						return
					}

					if len(req.Delay) > 0 {
						time.Sleep(delay(req.Delay))
					}

					for _, sio := range resp.PublishSocketIO {
						if sio.Data == nil {
							continue
						}
						log.Printf("[pubnub] http message %s %s %s ...", resp.Name, sio.Namespace, sio.Event)

						dataVal, err := sio.Data.Expr.Value(&bodyEvalCtx)
						if log.OnErr(err).Printf("[pubnub] http failed to broadcast: %v", err).HasErr() {
							return
						}

						msg := map[string]interface{}{
							"name": sio.Event,
							"ns":   sio.Namespace,
							"data": toObject(dataVal.AsString()),
						}

						log.Println("[pubnub] http broadcast ...")
						conn.Publish().Channel(p.client.channel[resp.Name]).Message(msg).Execute()
					}

					if len(timeout) == 0 && req.Ticker != nil && len(req.Ticker.Time) > 0 {
						time.Sleep(delay(req.Ticker.Time))
						continue
					}

					break
				}
			}()
		})
	}, true
}

// toObject takes in a JSON string and returns
// it as a map or slice if possible
func toObject(data string) interface{} {
	// for now we just brute force try...
	var asMap map[string]interface{}
	json.Unmarshal([]byte(data), &asMap)

	if len(asMap) > 0 {
		return asMap
	}

	var asSlice []interface{}
	json.Unmarshal([]byte(data), &asSlice)

	if len(asSlice) > 0 {
		return asSlice
	}

	return data
}
