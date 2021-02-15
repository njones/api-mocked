// +build pubnub

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	pngo "github.com/pubnub/go"
	"github.com/rs/xid"
)

const pubnubPluginName = "pubnub"

func init() {
	log.Println("[init] loading the PubNub plugin ...")
	plugins[pubnubPluginName] = new(pubnubPlugin)
}

type pnServerName string

type pubnubPlugin struct {
	isSetup bool
	client  struct {
		conn    map[string]*pngo.PubNub
		channel map[string]string
	}

	config map[string]pubnubConfig

	On map[string]func(string, string, interface{})
}

type pubnubConfig struct {
	Name         string         `hcl:"name,label"`
	PublishKey   *hcl.Attribute `hcl:"publish_key"`
	SubscribeKey *hcl.Attribute `hcl:"subscribe_key"`
	Channel      string         `hcl:"channel,optional"`
	UUID         string         `hcl:"uuid,optional"`
}

type pubnub struct {
	Name string `hcl:"name,label"`
	Desc string `hcl:"_-,optional"`

	SubscribeSocketIO []struct {
		Namespace string            `hcl:"ns,label"`
		Event     string            `hcl:"event,label"`
		Delay     string            `hcl:"delay,optional"`
		Broadcast []pubnubBroadcast `hcl:"broadcast_socketio,block"`
		Emit      []pubnubEmit      `hcl:"emit_socketio,block"`
	} `hcl:"subscribe,block"`

	PublishSocketIO []pubnubBroadcast `hcl:"broadcast_socketio,block"`
}

type pubnubBroadcast struct {
	Namespace string         `hcl:"ns,label"`
	Event     string         `hcl:"event,label"`
	Channel   string         `hcl:"channel,optional"`
	Data      *hcl.Attribute `hcl:"data"`
}

type pubnubEmit struct {
	Channel string         `hcl:"channel,optional"`
	Data    *hcl.Attribute `hcl:"data"`
}

func (p *pubnubPlugin) Setup() (err error) {
	log.Println("[pubnub] setup plugin ...")

	p.client.conn = make(map[string]*pngo.PubNub)
	p.client.channel = make(map[string]string)
	p.config = make(map[string]pubnubConfig)
	p.On = make(map[string]func(string, string, interface{}))

	return nil
}

func (p *pubnubPlugin) SetupConfig(svrName string, svrPlugins hcl.Body) (err error) {
	cfg := p.config[svrName]

	svrb, _, _ := svrPlugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       pubnubPluginName,
				LabelNames: []string{"name"},
			},
		},
	})

	if len(svrb.Blocks) == 0 {
		return
	}

	for _, block := range svrb.Blocks {
		var pnc pubnubConfig
		switch block.Type {
		case pubnubPluginName:
			gohcl.DecodeBody(block.Body, nil, &pnc)
			if len(block.Labels) > 0 {
				pnc.Name = block.Labels[0] // the same index as the LabelNames above...
			}
			p.config[svrName] = pnc
			cfg = p.config[svrName]
		}
	}

	if cfg.UUID == "" {
		cfg.UUID = xid.New().String()
		p.config[svrName] = cfg
	}

	publishKey, err := cfg.PublishKey.Expr.Value(&fileEvalCtx)
	if err != nil {
		return err
	}

	subscribeKey, err := cfg.SubscribeKey.Expr.Value(&fileEvalCtx)
	if err != nil {
		return err
	}

	var conf = pngo.NewConfig()
	conf.PublishKey = publishKey.AsString()
	conf.SubscribeKey = subscribeKey.AsString()
	conf.UUID = p.config[svrName].UUID

	p.client.conn[cfg.Name] = pngo.NewPubNub(conf)
	p.client.channel[cfg.Name] = cfg.Channel

	log.Printf("[pubnub] client %s (channel: %q uuid: %q) ...", cfg.Name, cfg.Channel, cfg.UUID)

	return nil
}

func (p *pubnubPlugin) SetupRoot(configPlugins hcl.Body) error {

	var listener = p.NewListener()

	cfgb, _, _ := configPlugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       pubnubPluginName,
				LabelNames: []string{"name"},
			},
		},
	})

	for _, block := range cfgb.Blocks {
		var pn pubnub
		switch block.Type {
		case pubnubPluginName:
			gohcl.DecodeBody(block.Body, nil, &pn)
			if len(block.Labels) > 0 {
				pn.Name = block.Labels[0] // the same index as the LabelNames above...
			}
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

				var uuid, ch, ns, event string
				var data interface{}
				if msg, ok := message.Message.(map[string]interface{}); ok {
					ch = message.Channel
					ns, _ = msg["ns"].(string)
					event, _ = msg["name"].(string)
					data, _ = msg["data"]
					uuid, _ = msg["uuid"].(string)
				}
				log.Printf("[pubnub] message %s %s %s (%s) ...", ch, ns, event, uuid)

				key := fmt.Sprintf("%s+%s+%s", ch, ns, event) // just smash together so we can have a unique event
				if key == "++" {
					continue
				}

				if _, ok := p.On[key]; ok {
					log.Println("[pubnub] execute message callback ...")
					if uuid == "" {
						uuid = message.Publisher
					}
					p.On[key](uuid, ns, data)
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
		log.Printf("[pubnub] SUB %q %q added ...", sio.Namespace, sio.Event)

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

				dataVal, dia := pub.Data.Expr.Value(&bodyEvalCtx)
				if dia.HasErrors() {
					for _, err := range dia.Errs() {
						log.Printf("[pubnub] callback failed to emit: %v", err)
					}
					return
				}

				msg := map[string]interface{}{
					"name": sid,
					"ns":   ns,
					"data": toObject(dataVal.AsString()),
				}

				log.Println("[pubnub] callback emit ...")
				_, status, err := conn.Publish().Channel(pub.Channel).Message(msg).Execute() // TODO(njones):look for errors
				log.Printf("[pubnub] broadcast status: %d", status.StatusCode)
				log.OnErr(err).Printf("[pubnub] error: %v", err)
			}

			for _, pub := range sio.Broadcast {
				if pub.Data == nil {
					continue
				}

				dataVal, dia := pub.Data.Expr.Value(&bodyEvalCtx)
				if dia.HasErrors() {
					for _, err := range dia.Errs() {
						log.Printf("[pubnub] callback failed to broadcast: %v", err)
					}
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

func (p *pubnubPlugin) MiddlewareHTTP(r Route, req RequestHTTP) (MiddlewareHTTP, bool) {
	reqb, _, _ := req.Plugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       pubnubPluginName,
				LabelNames: []string{"name"},
			},
		},
	})

	if len(reqb.Blocks) == 0 {
		return nil, false
	}

	var reqPubNub []pubnub
	for _, block := range reqb.Blocks {
		var pn pubnub
		switch block.Type {
		case pubnubPluginName:
			gohcl.DecodeBody(block.Body, nil, &pn)
			if len(block.Labels) > 0 {
				pn.Name = block.Labels[0]
			}
			reqPubNub = append(reqPubNub, pn)
		}
	}

	if len(reqPubNub) == 0 {
		return nil, false
	}

	var idx = int64(-1)
	var resps = reqPubNub
	if req.Order == "unordered" {
		rand.Seed(time.Now().UnixNano()) // doesn't have to be crypto-quality random here...
	}
	log.Printf("[pubnub] %s http response added ...", r.Path)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Print("[pubnub] starting http response ...")
			defer func() { next.ServeHTTP(w, r) }()

			log.Print("[pubnub] allow tick responses for 1m at most ...")
			timeoutTimer := time.NewTimer(1 * time.Minute) // HARDCODED FOR NOW
			timeout := timeoutTimer.C
			go func() {
				defer timeoutTimer.Stop()

				if len(resps) > 1 {
					log.Print("[pubnub] starting tick ...")
				}
				for {
					var x int64
					var useTxt string
					switch req.Order {
					case "random":
						x = rand.Int63n(int64(len(resps) * 2))
						useTxt = `using "random" ...`
					case "unordered":
						x = atomic.AddInt64(&idx, 1)
						if int(x)%len(resps) == 0 {
							rand.Shuffle(len(resps), func(i, j int) { resps[i], resps[j] = resps[j], resps[i] })
						}
						useTxt = `using "unordered" ...`
					default:
						x = atomic.AddInt64(&idx, 1)
						useTxt = `using "ordered" ...`
					}
					if len(resps) > 1 {
						log.Println("[pubnub]", useTxt)
					}

					log.Print(`[pubnub] collecting the response ...`)
					resp := resps[int(x)%len(resps)]
					conn, ok := p.client.conn[resp.Name]
					if !ok {
						return
					}

					log.Print(`[pubnub] applying the delay ...`)
					if len(req.Delay) > 0 {
						time.Sleep(delay(req.Delay))
					}

					log.Print(`[pubnub] publishing as socketio ...`)
					for _, sio := range resp.PublishSocketIO {
						if sio.Data == nil {
							continue
						}
						log.Printf("[pubnub] http message %s %s %s ...", resp.Name, sio.Namespace, sio.Event)

						dataVal, dia := sio.Data.Expr.Value(&bodyEvalCtx)
						if dia.HasErrors() {
							for _, err := range dia.Errs() {
								log.Printf("[pubnub] http failed to broadcast: %v", err)
							}
							return
						}

						msg := map[string]interface{}{
							"name": sio.Event,
							"ns":   sio.Namespace,
							"data": toObject(dataVal.AsString()),
						}

						log.Println("[pubnub] http broadcast ...")
						_, status, err := conn.Publish().Channel(p.client.channel[resp.Name]).Message(msg).Execute()
						log.Printf("[pubnub] broadcast status: %d", status.StatusCode)
						log.OnErr(err).Printf("[pubnub] error: %v", err)
					}

					if len(resps) > 0 {
						log.Print(`[pubnub] checking ticker (repeat) ...`)
					}
					if len(timeout) == 0 && req.Ticker != nil && len(req.Ticker.Time) > 0 {
						time.Sleep(delay(req.Ticker.Time))
						log.Print(`[pubnub] continue ...`)
						continue
					}

					log.Print(`[pubnub] ending http response ...`)
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
