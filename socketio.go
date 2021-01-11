package main

import (
	"log"

	sktio "github.com/ambelovsky/gosf-socketio"
	"github.com/ambelovsky/gosf-socketio/transport"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

var sktioChanMap = map[string]*sktio.Channel{}

type m struct {
	mm map[string]interface{}
}

// TODO(njones): add this to an array, so that we can have more than one connection at a time.
var conn = sktio.NewServer(transport.GetDefaultWebsocketTransport())
var cvt = new(converter)
var convertToJSON = func(body hcl.Body) map[string]interface{} {
	jsonData, err := cvt.convertBody(body.(*hclsyntax.Body))
	if err != nil {
		log.Println("emit:", err)
	}

	return jsonData
}

func _socketio(websockets []socketio) *sktio.Server {
	log.Println("loading socketio...")

	for _, ws := range websockets {
		log.Printf("[ws] adding %q", ws.Event)

		// wrap this call in a function so we can pass the loop variable properly
		func(_ws socketio) {

			conn.On(_ws.Event, func(channel *sktio.Channel, args interface{}) {
				log.Printf("on %s...", _ws.Event)

				// for _, join := range _ws.Join {
				// 	channel.Join(join.Room)
				// }
				// for _, leave := range _ws.Leave {
				// 	channel.Leave(leave.Room)
				// }

				for _, emit := range _ws.Emit {
					channel.Emit(emit.Event, convertToJSON(emit.Args))
				}
				for _, broadcast := range _ws.Broadcast {
					conn.BroadcastTo(broadcast.Room, broadcast.Event, convertToJSON(broadcast.Args))
				}
				for _, broadcast := range _ws.BroadcastAll {
					conn.BroadcastToAll(broadcast.Event, convertToJSON(broadcast.Args))
				}
			})

		}(ws) // passing the loop variable properly

	}

	return conn
}
