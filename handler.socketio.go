// +build socketio

package main

import (
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"

	sktio "github.com/ambelovsky/gosf-socketio"
	"github.com/ambelovsky/gosf-socketio/transport"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

func init() {
	log.Println("[init] loading the socket.io plugin ...")
	plugins["socketio"] = new(socketioPlugin)
}

var cvt = new(converter)
var convertToJSON = func(body hcl.Body) map[string]interface{} {
	jsonData, err := cvt.convertBody(body.(*hclsyntax.Body))
	if log.OnErr(err).Printf("[socketio] failed json body convert: %v", err).HasErr() {
		return nil
	}

	return jsonData
}

type socketioPlugin struct {
	conn map[string]*sktio.Server
}

func (p *socketioPlugin) Setup(config *Config) error {
	log.Println("[socketio] setup plugin ...")

	p.conn = make(map[string]*sktio.Server)

	for _, sio := range config.Servers {
		if sio.SocketIO == nil {
			continue
		}

		p.conn[sio.Name] = sktio.NewServer(transport.GetDefaultWebsocketTransport())
	}

	for _, ws := range config.Websockets {
		for _, sio := range ws.SocketIO {
			p.Subscribe(sio)
		}
	}

	return nil
}

func (p *socketioPlugin) Subscribe(sio socketio) {
	log.Printf("[socketio] subcribe to event %q ...", sio.Event)

	p.conn[sio.Name].On(sio.Event, func(channel *sktio.Channel, args interface{}) {

		for _, emit := range sio.Emit {
			log.Println("[socketio] emit ...")
			channel.Emit(emit.Event, convertToJSON(emit.Args))
		}

		for _, broadcast := range sio.Broadcast {
			log.Println("[socketio] broadcast ...")
			p.conn[sio.Name].BroadcastTo(broadcast.Room, broadcast.Event, convertToJSON(broadcast.Args))
		}

		for _, broadcast := range sio.BroadcastAll {
			log.Println("[socketio] broadcast all ...")
			p.conn[sio.Name].BroadcastToAll(broadcast.Event, convertToJSON(broadcast.Args))
		}
	})
}

func (p *socketioPlugin) Serve(r route, req request) (func(http.Handler) http.Handler, bool) {
	if len(r.SocketIO) == 0 {
		return nil, false
	}

	var idx = int64(-1)
	var resps = req.SocketIO
	if req.Order == "unordered" {
		rand.Seed(time.Now().UnixNano()) // doesn't have to be crypto-quality random here...
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { next.ServeHTTP(w, r) }()

			go func() {
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
					log.Println("[socketio] sending event ...")

					if len(req.Delay) > 0 {
						time.Sleep(delay(req.Delay))
					}

					for _, broadcast := range resp.Broadcast {
						data := convertToJSON(broadcast.Args)
						if data != nil {
							log.Println("[socketio] http broadcast ...")
							p.conn[resp.Name].BroadcastTo(broadcast.Room, broadcast.Event, data)
						}
					}

					for _, broadcast := range resp.BroadcastAll {
						data := convertToJSON(broadcast.Args)
						if data != nil {
							log.Println("[socketio] http broadcast all ...")
							p.conn[resp.Name].BroadcastToAll(broadcast.Event, data)
						}
					}

					if req.Ticker != nil && len(req.Ticker.Time) > 0 {
						time.Sleep(delay(req.Ticker.Time))
						continue
					}

					break
				}

			}()
		})
	}, true
}
