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

	Version string         `hcl:"version,optional"`
	System  *system        `hcl:"system,block"`
	Servers []serverConfig `hcl:"server,block"`

	Routes []route `hcl:"path,block"`

	NotFound *struct {
		Response response `hcl:"response,block"`
	} `hcl:"notfound,block"`
	MethodNotAllowed *struct {
		Response response `hcl:"response,block"`
	} `hcl:"methodnotallowed,block"`

	Plugins hcl.Body `hcl:",remain"`
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
	JWT       *jwtConfig `hcl:"jwt,block"`
	SSL       *sslConfig `hcl:"ssl,block"`

	Plugins hcl.Body `hcl:",remain"`
}

// basic auth config options
type baConfig struct {
	User string `hcl:"username,optional"`
	Pass string `hcl:"password,optional"`
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
	CACrt   string `hcl:"ca_cert,optional"`
	CAKey   string `hcl:"ca_key,optional"`
	Crt     string `hcl:"cert,optional"`
	Key     string `hcl:"key,optional"`
	LetsEnc *struct {
		Hosts []string       `hcl:"hosts"`
		Email *hcl.Attribute `hcl:"email"`
	} `hcl:"lets_encrypt,block"`
}

type route struct {
	Path string     `hcl:"path,label"`
	Desc string     `hcl:"_-,optional"`
	CORS *corsBlock `hcl:"cors,block"`

	Request []request `hcl:"request,block"`

	Plugins hcl.Body `hcl:",remain"`
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

	JWT      *jwtRequest       `hcl:"jwt,block"`
	Headers  *headers          `hcl:"header,block"`
	Posted   map[string]string `hcl:"post_values,optional"`
	Response []response        `hcl:"response,block"`

	Plugins hcl.Body `hcl:",remain"`
}

type response struct {
	Status  string         `hcl:"status,label"`
	Headers *headers       `hcl:"header,block"`
	JWT     *jwtResponse   `hcl:"jwt,block"`
	Body    *hcl.Attribute `hcl:"body"`
	PubKey  *string        `hcl:"hpkp"`
}

type jwtRequest struct {
	Name  string `hcl:"name,label"`
	Input string `hcl:"input,label"`
	Key   string `hcl:"key,label"`

	Validate *bool  `hcl:"validate"`
	Prefix   string `hcl:"prefix,optional"`
}

type jwtResponse struct {
	Name   string `hcl:"name,label"`
	Key    string `hcl:"key,label"`
	Output string `hcl:"output,label"`

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
