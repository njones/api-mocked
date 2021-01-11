package main

import (
	"encoding/json"
	"log"
	"time"

	pngo "github.com/pubnub/go"
)

type pnClient struct {
	conn    map[string]*pngo.PubNub
	channel map[string]string
}

func newPubNubClient() *pnClient {
	return &pnClient{
		conn:    make(map[string]*pngo.PubNub),
		channel: make(map[string]string),
	}
}

func (c *pnClient) Add(name, channel, publishKey, subscribeKey, uuid string) {
	var conf = pngo.NewConfig()
	conf.PublishKey = publishKey
	conf.SubscribeKey = subscribeKey
	conf.UUID = uuid
	c.conn[name] = pngo.NewPubNub(conf)
	c.channel[name] = channel
}

func (c *pnClient) Get(name string) *pngo.PubNub { return c.conn[name] }

func _pubnub(config *Config) {
	go func() {
		clientPN := <-config.done.loadPubNubConfig

		var msgDo = make(map[string]map[string]func(interface{}, string, string))

		listener := pngo.NewListener()
		go func() {
			for {
				select {
				case message := <-listener.Message:
					if m2, ok := msgDo[message.Channel]; ok {
						if mmm, ok := message.Message.(map[string]interface{}); ok {
							if fn, ok := m2[mmm["ns"].(string)+"#"+mmm["name"].(string)]; ok {
								fn(mmm["data"], mmm["ns"].(string), message.Publisher)
							}
						}
					}
				}
			}
		}()

		for _, websocket := range config.Websockets {
			for _, pn := range websocket.PubNub {
				if conn, ok := clientPN.conn[pn.Name]; ok {
					conn.AddListener(listener)

					for _, sio := range pn.SubscribeSocketIO {
						conn.Subscribe().Channels([]string{clientPN.channel[pn.Name]}).Execute()
						msgDo[clientPN.channel[pn.Name]] = map[string]func(interface{}, string, string){
							sio.Namespace + "#" + sio.Event: func(inMsg interface{}, ns string, sid string) {
								if sio.Delay != "" {
									if d, err := time.ParseDuration(sio.Delay); err == nil {
										time.Sleep(d)
									} else {
										log.Println(err)
									}
								}

								for _, pub := range sio.Emit {

									var data string
									if pub.Data != nil {
										ctyText, err := pub.Data.Expr.Value(&bodyContext)
										if err != nil {
											log.Fatal("cty pubnub data:", err)
										}
										data = ctyText.AsString()
									}

									var dataMap map[string]interface{}
									json.Unmarshal([]byte(data), &dataMap)

									var dataArray []interface{}
									json.Unmarshal([]byte(data), &dataArray)

									msg := map[string]interface{}{
										"name": sid,
										"ns":   ns,
										"data": data,
									}
									// TODO(njones):look for errors
									conn.Publish().Channel(pub.Channel).Message(msg).Execute()
								}

								for _, pub := range sio.Broadcast {

									var data string
									if pub.Data != nil {
										ctyText, err := pub.Data.Expr.Value(&bodyContext)
										if err != nil {
											log.Fatal("cty pubnub data:", err)
										}
										data = ctyText.AsString()
									}

									var dataMap map[string]interface{}
									json.Unmarshal([]byte(data), &dataMap)

									var dataArray []interface{}
									json.Unmarshal([]byte(data), &dataArray)

									msg := map[string]interface{}{
										"name": pub.Event,
										"ns":   pub.Namespace,
										"data": data,
									}
									// TODO(njones):look for errors
									conn.Publish().Channel(pub.Channel).Message(msg).Execute()
								}
							},
						}
					}
				}
			}
		}
	}()
}
