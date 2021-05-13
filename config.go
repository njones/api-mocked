package main

import (
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/afero"
	"github.com/zclconf/go-cty/cty"
)

// serviceControl controls stopping and starting HTTP services
type serviceControl struct {
	reload, shutdown chan struct{}
}

// reloadDrain is called when shutting down all of the running
// services. This sends a stop to gracefully stop them all.
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
		os    afero.Fs
		files []string

		svrStart        time.Time
		svrCfgLoad      time.Time
		svrCfgLoadValid bool // says if the last reload was successful
	}
	serviceControl

	Version string       `hcl:"version,optional"`
	System  *system      `hcl:"system,block"`
	Servers []ConfigHTTP `hcl:"http,block"`

	Routes []Route     `hcl:"path,block"`
	Texts  []TextBlock `hcl:"text,block"`

	NotFound *struct {
		Response ResponseHTTP `hcl:"response,block"`
	} `hcl:"notfound,block"`
	MethodNotAllowed *struct {
		Response ResponseHTTP `hcl:"response,block"`
	} `hcl:"methodnotallowed,block"`

	Plugins hcl.Body `hcl:",remain"`
}

// system holds all of the internal system dependent configs
type system struct {
	LogDir *string `hcl:"log_dir"` // the name of the directory to save reload logs to
}

// headerData is the type used for storing header KV data
type headerData map[string][]cty.Value

// headers holds all headers
type headers struct {
	Data headerData `hcl:",remain"`
}

// ConfigHTTP hold configurations for HTTP services
type ConfigHTTP struct {
	Name      string       `hcl:"name,label"`
	Host      string       `hcl:"host,optional"`
	HTTP2     bool         `hcl:"http2_only,optional"`
	BasicAuth *configBA    `hcl:"basic_auth,block"`
	JWT       *configJWT   `hcl:"jwt,block"`
	SSL       *configSSL   `hcl:"ssl,block"`
	Proxy     *configProxy `hcl:"proxy,block"`

	Plugins hcl.Body `hcl:",remain"`
}

// configBA are basic auth config options
type configBA struct {
	User string `hcl:"username,optional"`
	Pass string `hcl:"password,optional"`
	Relm string `hcl:"relm,optional"`
}

// configJWT are JWT config options
type configJWT struct {
	Name   string         `hcl:"name,label"`
	Alg    string         `hcl:"algo"`
	Typ    *string        `hcl:"typ"`
	Key    *hcl.Attribute `hcl:"private_key"`
	Secret *hcl.Attribute `hcl:"secret"`
}

// configSSL are SSL config options
type configSSL struct {
	CACrt   string `hcl:"ca_cert,optional"`
	CAKey   string `hcl:"ca_key,optional"`
	Crt     string `hcl:"cert,optional"`
	Key     string `hcl:"key,optional"`
	LetsEnc *struct {
		Hosts []string       `hcl:"hosts"`
		Email *hcl.Attribute `hcl:"email"`
	} `hcl:"lets_encrypt,block"`
}

// configProxy are proxy config options
type configProxy struct {
	Name    string   `hcl:"name,label"`
	URL     string   `hcl:"url"`
	Mode    string   `hcl:"mode,optional"`
	Headers *headers `hcl:"headers,block"`

	_url *url.URL
}

// MiddlewareHTTP is the middleware type
type MiddlewareHTTP func(http.Handler) http.Handler

// TextBlock holds data that can be accessed by the text(name, arg...) function
type TextBlock struct {
	Name string         `hcl:"name,label"`
	Data *hcl.Attribute `hcl:"data"`
}

// Route holds configurations for each HTTP path
type Route struct {
	Path  string      `hcl:"path,label"`
	Desc  string      `hcl:"_-,optional"`
	CORS  *routeCORS  `hcl:"cors,block"`
	Proxy *routeProxy `hcl:"proxy,block"`

	Request []RequestHTTP `hcl:"request,block"`

	Plugins hcl.Body `hcl:",remain"`
}

// RequestHTTP holds HTTP request configuration options
type RequestHTTP struct {
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

	JWT     *requestJWT       `hcl:"jwt,block"`
	Headers *headers          `hcl:"header,block"`
	Posted  map[string]string `hcl:"post_values,optional"`

	Response []ResponseHTTP `hcl:"response,block"`

	Plugins hcl.Body `hcl:",remain"`

	seed int64
	rand *rand.Rand
}

// ResponseHTTP holds HTTP response options
type ResponseHTTP struct {
	Status  string         `hcl:"status,label"`
	Headers *headers       `hcl:"header,block"`
	JWT     *responseJWT   `hcl:"jwt,block"`
	Body    *hcl.Attribute `hcl:"body"`
	PubKey  *string        `hcl:"hpkp"`

	Plugins hcl.Body `hcl:",remain"`
}

// routeCORS holds options for CORS within a route (or path)
type routeCORS struct {
	AllowOrigin      string   `hcl:"allow_origin,label"`
	AllowMethods     []string `hcl:"allow_methods,optional"`
	AllowHeaders     []string `hcl:"allow_headers,optional"`
	MaxAge           *int     `hcl:"max_age"`
	AllowCredentials *bool    `hcl:"allow_credentials"`
}

// requestJWT hold configurations for how JWTs can be used
// during the HTTP request process
type requestJWT struct {
	Name  string `hcl:"name,label"`
	Input string `hcl:"input,label"`
	Key   string `hcl:"key,label"`

	Validate *bool  `hcl:"validate"`
	Prefix   string `hcl:"prefix,optional"`

	KeyVals map[string]*hcl.Attribute `hcl:",remain"` // key value pairs to match on
}

// responseJWT hold configurations for how JWTs can be used
// during the HTTP response process
type responseJWT struct {
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
	Roles      []string          `hcl:"roles,optional" json:"roles,omitempty"`
	AuthType   []string          `hcl:"auth_type,optional" json:"auth_type,omitempty"`
	Payload    map[string]string `hcl:",remain" json:"payload,omitempty"`

	_ctx *hcl.EvalContext
}

// routeProxy holds configurations for Proxy servers that
// can be used during the routing process
type routeProxy struct {
	Name    string   `hcl:"name,label"`
	Headers *headers `hcl:"headers,block"`
}
