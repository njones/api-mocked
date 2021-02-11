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
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const socketioPluginName = "socketio"

func init() {
	log.Println("[init] loading the socket.io plugin ...")
	plugins[socketioPluginName] = new(socketioPlugin)
}

type socketioPlugin struct {
	conn   map[string]*sktio.Server
	config map[string]socketioConfig
}

type socketioConfig struct {
	Name string `hcl:"name,label"`
	UUID string `hcl:"id,optional"`
}

type socketio struct {
	Name  string `hcl:"name,label"`
	Event string `hcl:"event,label"`
	Desc  string `hcl:"_-,optional"`

	Broadcast []struct {
		Room  string   `hcl:"room,label"`
		Event string   `hcl:"event,label"`
		Args  hcl.Body `hcl:",remain"`
	} `hcl:"broadcast,block"`
	BroadcastAll []struct {
		Event string   `hcl:"event,label"`
		Args  hcl.Body `hcl:",remain"`
	} `hcl:"broadcast_all,block"`
	Emit []struct {
		Event string   `hcl:"event,label"`
		Args  hcl.Body `hcl:",remain"`
	} `hcl:"emit,block"`
}

var cvt = new(converter)
var convertToJSON = func(body hcl.Body) map[string]interface{} {
	jsonData, err := cvt.convertBody(body.(*hclsyntax.Body))
	if log.OnErr(err).Printf("[socketio] failed json body convert: %v", err).HasErr() {
		return nil
	}

	return jsonData
}

func (p *socketioPlugin) Setup(config *Config) error {
	log.Println("[socketio] setup plugin ...")

	p.conn = make(map[string]*sktio.Server)
	p.config = make(map[string]socketioConfig)

	for _, svr := range config.Servers {

		svrb, _, _ := svr.Plugins.PartialContent(&hcl.BodySchema{
			Blocks: []hcl.BlockHeaderSchema{
				{
					Type:       socketioPluginName,
					LabelNames: []string{"name"},
				},
			},
		})

		if len(svrb.Blocks) == 0 {
			continue
		}

		for _, block := range svrb.Blocks {
			var sioc socketioConfig
			switch block.Type {
			case socketioPluginName:
				gohcl.DecodeBody(block.Body, nil, &sioc)
				if len(block.Labels) > 0 {
					sioc.Name = block.Labels[0] // the same index as the LabelNames above...
				}
				p.config[svr.Name] = sioc
			}
		}

		p.conn[svr.Name] = sktio.NewServer(transport.GetDefaultWebsocketTransport())
	}

	cfgb, _, _ := config.Plugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       socketioPluginName,
				LabelNames: []string{"name"},
			},
		},
	})

	for _, block := range cfgb.Blocks {
		var sio socketio
		switch block.Type {
		case socketioPluginName:
			gohcl.DecodeBody(block.Body, nil, &sio)
			if len(block.Labels) > 0 {
				sio.Name = block.Labels[0] // the same index as the LabelNames above...
			}
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

func (p *socketioPlugin) Serve(r route, req request) (middleware, bool) {
	reqb, _, _ := req.Plugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       socketioPluginName,
				LabelNames: []string{"name"},
			},
		},
	})

	if len(reqb.Blocks) == 0 {
		return nil, false
	}

	var reqSocketIO []socketio
	for _, block := range reqb.Blocks {
		var sio socketio
		switch block.Type {
		case socketioPluginName:
			gohcl.DecodeBody(block.Body, nil, &sio)
			if len(block.Labels) > 0 {
				sio.Name = block.Labels[0]
			}
			reqSocketIO = append(reqSocketIO, sio)
		}
	}

	if len(reqSocketIO) == 0 {
		return nil, false
	}

	var idx = int64(-1)
	var resps = reqSocketIO
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
