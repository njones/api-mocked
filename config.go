package main

import (
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

type serviceControl struct {
	reload, shutdown chan struct{}
}

func (sc serviceControl) reloadDrain(shutdown chan struct{}) {
	for {
		select {
		case <-sc.reload: // there are mutiple reloads in quick sucession that need to be captured
		case <-shutdown:
			return
		}
	}
}

// Config holds all of the configuration options of the MockServer
type Config struct {
	internal struct {
		os   afero.Fs
		file string

		svrStart        time.Time
		svrCfgLoad      time.Time
		svrCfgLoadValid bool // says if the last reload was successful
	}
	serviceControl

	Version string `hcl:"version,optional"`

	System  *system        `hcl:"system,block"`
	Servers []serverConfig `hcl:"server,block"`

	Routes     []route     `hcl:"path,block"`
	Websockets []websocket `hcl:"websocket,block"`

	NotFound *struct {
		Response response `hcl:"response,block"`
	} `hcl:"notfound,block"`
	MethodNotAllowed *struct {
		Response response `hcl:"response,block"`
	} `hcl:"methodnotallowed,block"`
}

type system struct {
	LogDir *string `hcl:"log_dir"` // the name of the directory to save reload logs to
}

type headers struct {
	Data map[string][]string `hcl:",remain"`
}

type corsBlock struct {
	AllowOrigin      string   `hcl:"allow_origin,label"`
	AllowMethods     []string `hcl:"allow_methods,optional"`
	AllowHeaders     []string `hcl:"allow_headers,optional"`
	MaxAge           *int     `hcl:"max_age"`
	AllowCredentials *bool    `hcl:"Allow_Credentials"`
}

// server config options
type serverConfig struct {
	Name      string     `hcl:"name,label"`
	Host      string     `hcl:"host,optional"`
	HTTP2     bool       `hcl:"http2_only,optional"`
	BasicAuth *baConfig  `hcl:"basic_auth,block"`
	PubNub    *pnConfig  `hcl:"pubnub,block"`
	SocketIO  *sioConfig `hcl:"socketio,block"`
	JWT       *jwtConfig `hcl:"jwt,block"`
	SSL       *sslConfig `hcl:"ssl,block"`
}

// basic auth config options
type baConfig struct {
	User string `hcl:"username,optional"`
	Pass string `hcl:"password,optional"`
}

type pnConfig struct {
	Name         string         `hcl:"name,label"`
	PublishKey   *hcl.Attribute `hcl:"publish_key"`
	SubscribeKey *hcl.Attribute `hcl:"subscribe_key"`
	Channel      string         `hcl:"channel,optional"`
	UUID         string         `hcl:"uuid,optional"`
}

type sioConfig struct {
	Name string `hcl:"name,label"`
	UUID string `hcl:"id,optional"`
}

// JWT config options
type jwtConfig struct {
	Name   string         `hcl:"name,label"`
	Alg    string         `hcl:"algo"`
	Typ    *string        `hcl:"typ"`
	Key    *hcl.Attribute `hcl:"private_key"`
	Secret *hcl.Attribute `hcl:"secret"`
}

// SSL config options
type sslConfig struct {
	CACrt   string   `hcl:"ca_cert,optional"`
	CAKey   string   `hcl:"ca_key,optional"`
	Crt     string   `hcl:"cert,optional"`
	Key     string   `hcl:"key,optional"`
	LetsEnc []string `hcl:"lets_encrypt,optional"`
}

type route struct {
	Path string     `hcl:"path,label"`
	Desc string     `hcl:"_-,optional"`
	CORS *corsBlock `hcl:"cors,block"`

	Request  []request  `hcl:"request,block"`
	SocketIO []socketio `hcl:"socketio,block"`
	PubNub   []pubnub   `hcl:"pubnub,block"`
}

type websocket struct {
	PubNub   []pubnub   `hcl:"pubnub,block"`
	SocketIO []socketio `hcl:"socketio,block"`
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

	JWT      *jwtRequest `hcl:"jwt,block"`
	Headers  *headers    `hcl:"header,block"`
	Response []response  `hcl:"response,block"`
	SocketIO []socketio  `hcl:"socketio,block"`
	PubNub   []pubnub    `hcl:"pubnub,block"`
}

type response struct {
	Status  string         `hcl:"status,label"`
	Headers *headers       `hcl:"header,block"`
	Body    *hcl.Attribute `hcl:"body"`
	JWT     *jwtResponse   `hcl:"jwt,block"`
	PubKey  *string        `hcl:"hpkp"`
}

type jwtRequest struct {
	Name  string `hcl:"name,label"`
	Input string `hcl:"input,label"`
	Key   string `hcl:"key,label"`

	Validate bool   `hcl:"validate,optional"`
	Prefix   string `hcl:"prefix,optional"`
}

type jwtResponse struct {
	Name   string `hcl:"name,label"`
	Output string `hcl:"output,label"`
	Key    string `hcl:"key,label"`

	Subject    *hcl.Attribute    `hcl:"sub" json:"sub"`
	Issuers    *hcl.Attribute    `hcl:"iss" json:"iss"`
	Audience   *hcl.Attribute    `hcl:"aud" json:"aud"`
	Expiration *hcl.Attribute    `hcl:"exp" json:"exp"`
	NotBefore  *hcl.Attribute    `hcl:"nbf" json:"nbf"`
	IssuedAt   *hcl.Attribute    `hcl:"iat" json:"iat"`
	JWTID      *hcl.Attribute    `hcl:"jti" json:"jti"`
	Roles      []string          `hcl:"roles,optional" json:"roles,optional"`
	AuthType   []string          `hcl:"auth_type,optional" json:"auth_type,optional"`
	Payload    map[string]string `hcl:",remain"`

	_hclVarMap map[string]map[string]cty.Value
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
