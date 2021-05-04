package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

type Plugin interface {
	Setup() error
	Version(int32) int32 // the version that API-Mocked supports is passed, the version the plugin supports
	Metadata() string    // a string with the plugin semver, author
	SetupRoot(hcl.Body) error
	SetupConfig(string, hcl.Body) error // can be called multiple times
}

type HTTP struct {
	Name      string `hcl:"name,label"`
	Host      string `hcl:"host,optional"`
	HTTP2     bool   `hcl:"http2_only,optional"`
	BasicAuth *struct {
		User string `hcl:"username,optional"`
		Pass string `hcl:"password,optional"`
		Relm string `hcl:"relm,optional"`
	} `hcl:"basic_auth,block"`
	JWT *struct {
		Name   string         `hcl:"name,label"`
		Alg    string         `hcl:"algo"`
		Typ    *string        `hcl:"typ"`
		Key    *hcl.Attribute `hcl:"private_key"`
		Secret *hcl.Attribute `hcl:"secret"`
	} `hcl:"jwt,block"`
	SSL *struct {
		CACrt   string `hcl:"ca_cert,optional"`
		CAKey   string `hcl:"ca_key,optional"`
		Crt     string `hcl:"cert,optional"`
		Key     string `hcl:"key,optional"`
		LetsEnc *struct {
			Hosts []string       `hcl:"hosts"`
			Email *hcl.Attribute `hcl:"email"`
		} `hcl:"lets_encrypt,block"`
	} `hcl:"ssl,block"`
	Proxy *struct {
		Name    string `hcl:"name,label"`
		URL     string `hcl:"url"`
		Mode    string `hcl:"mode,optional"`
		Headers *struct {
			Data map[string][]cty.Value
		} `hcl:"header,block"`
	} `hcl:"proxy,block"`
}
