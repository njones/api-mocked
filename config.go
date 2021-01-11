package main

import (
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
)

// Config holds all of the configuration options of the MockServer
type Config struct {
	internal struct {
		fileLoadPath string

		serverStart      time.Time
		serverConfigLoad time.Time
	}

	System *struct {
		RunName   *string `hcl:"run_name"` // the name of the backup running config to use
		ErrorsDir *string `hcl:"dir"`      // the name of the directory to save issues to
	} `hcl:"system,block"`

	Version      string         `hcl:"version,optional"`
	Server       []serverConfig `hcl:"server,block"`
	PubNubConfig []pubnubConfig `hcl:"pubnub,block"`
	Routes       []route        `hcl:"path,block"`
	Websockets   []websocket    `hcl:"websocket,block"`
	NotFound     *struct {
		Response response `hcl:"response,block"`
	} `hcl:"notfound,block"`
	MethodNotAllowed *struct {
		Response response `hcl:"response,block"`
	} `hcl:"methodnotallowed,block"`

	reload, shutdown chan struct{}

	done loading
}

type loading struct {
	loadPubNubConfig chan *pnClient
}

type headers struct {
	Data map[string][]string `hcl:",remain"`
}

func (h *headers) Keys() ([]string, int) {
	var keys []string
	var varKeys int

	if h == nil {
		return nil, 0
	}

	for k := range h.Data {
		keys = append(keys, k)
		if strings.HasPrefix(k, "var.") {
			varKeys++
		}
	}
	return keys, varKeys
}

type corsBlock struct {
	AllowOrigin      string   `hcl:"allow_origin,label"`
	AllowMethods     []string `hcl:"allow_methods,optional"`
	AllowHeaders     []string `hcl:"allow_headers,optional"`
	MaxAge           *int     `hcl:"max_age"`
	AllowCredentials *bool    `hcl:"Allow_Credentials"`
}

type jwtReqBlock struct {
	Name  string `hcl:"name,label"`
	Input string `hcl:"input,label"`
	Key   string `hcl:"key,label"`

	Validate bool   `hcl:"validate,optional"`
	Prefix   string `hcl:"prefix,optional"`
}

type jwtRespBlock struct {
	Name   string `hcl:"name,label"`
	Output string `hcl:"output,label"`
	Key    string `hcl:"key,label"`

	Issuers    *hcl.Attribute    `hcl:"iss"`
	Subject    *hcl.Attribute    `hcl:"sub"`
	Audience   *hcl.Attribute    `hcl:"aud"`
	Expiration *hcl.Attribute    `hcl:"exp"`
	NotBefore  *hcl.Attribute    `hcl:"nbf"`
	IssuedAt   *hcl.Attribute    `hcl:"iat"`
	JWTID      *hcl.Attribute    `hcl:"jti"`
	Roles      []string          `hcl:"roles,optional"`
	AuthType   []string          `hcl:"auth_type,optional"`
	Payload    map[string]string `hcl:",remain"`
}

type baConfig struct {
	User string `hcl:"username,optional"`
	Pass string `hcl:"password,optional"`
}

type serverConfig struct {
	Name      string     `hcl:"name,label"`
	Host      string     `hcl:"host,optional"`
	HTTP2     bool       `hcl:"http2_only,optional"`
	SSL       *sslConfig `hcl:"ssl,block"`
	JWT       *jwtConfig `hcl:"jwt,block"`
	BasicAuth *baConfig  `hcl:"basic_auth,block"`
}

type sslConfig struct {
	CACrt   string   `hcl:"ca_cert,optional"`
	CAKey   string   `hcl:"ca_key,optional"`
	Crt     string   `hcl:"cert,optional"`
	Key     string   `hcl:"key,optional"`
	LetsEnc []string `hcl:"lets_encrypt,optional"`
}

type jwtConfig struct {
	Name   string         `hcl:"name,label"`
	Alg    string         `hcl:"algo"`
	Typ    *string        `hcl:"typ"`
	Key    *hcl.Attribute `hcl:"private_key,optional"`
	Secret string         `hcl:"secret,optional"`
}

type pubnubConfig struct {
	Name         string         `hcl:"name,label"`
	PublishKey   *hcl.Attribute `hcl:"publish_key"`
	SubscribeKey *hcl.Attribute `hcl:"subscribe_key"`
	Channel      string         `hcl:"channel,optional"`
	UUID         string         `hcl:"uuid,optional"`
}

type route struct {
	Path     string     `hcl:"path,label"`
	Desc     string     `hcl:"_-,optional"`
	CORS     *corsBlock `hcl:"cors,block"`
	Request  []request  `hcl:"request,block"`
	SocketIO []socketio `hcl:"socketio,block"`
}

type websocket struct {
	PubNub []pubnub `hcl:"pubnub,block"`
}

type request struct {
	Method string `hcl:"method,label"`

	Ticker *struct {
		Time  string `hcl:"time,label"`
		Limit *struct {
			Time  *hcl.Attribute `hcl:"time,optional"`
			Count *int           `hcl:"count,optional"`
			Loops *int           `hcl:"loops,optional"`
		} `hcl:"limit,block"`
	} `hcl:"ticker,block"`
	Order string `hcl:"order,optional"`
	Delay string `hcl:"delay,optional"`

	JWT      *jwtReqBlock `hcl:"jwt,block"`
	Headers  *headers     `hcl:"header,block"`
	Response []response   `hcl:"response,block"`
	SocketIO []socketio   `hcl:"socketio,block"`
	PubNub   []pubnub     `hcl:"pubnub,block"`
}

type response struct {
	Status  string         `hcl:"status,label"`
	Headers *headers       `hcl:"header,block"`
	Body    *hcl.Attribute `hcl:"body"`
	JWT     *jwtRespBlock  `hcl:"jwt,block"`
	PubKey  *string        `hcl:"hpkp"`
}

type pnBroadcast struct {
	Namespace string         `hcl:"ns,label"`
	Event     string         `hcl:"event,label"`
	Channel   string         `hcl:"channel,optional"`
	Data      *hcl.Attribute `hcl:"data"`
}

type pnEmit struct {
	Channel string         `hcl:"channel,optional"`
	Data    *hcl.Attribute `hcl:"data"`
}

type pubnub struct {
	Name string `hcl:"name,label"`
	Desc string `hcl:"_-,optional"`

	SubscribeSocketIO []struct {
		Namespace string        `hcl:"ns,label"`
		Event     string        `hcl:"event,label"`
		Delay     string        `hcl:"delay"`
		Broadcast []pnBroadcast `hcl:"broadcast_socketio,block"`
		Emit      []pnEmit      `hcl:"emit_socketio,block"`
	} `hcl:"subscribe,block"`

	PublishSocketIO []pnBroadcast `hcl:"broadcast_socketio,block"`
}

type socketio struct {
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
