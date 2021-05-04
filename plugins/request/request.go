package request

import (
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

type HTTP struct {
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

	Headers *struct {
		Data map[string][]cty.Value
	} `hcl:"header,block"`
	JWT *struct {
		Name  string `hcl:"name,label"`
		Input string `hcl:"input,label"`
		Key   string `hcl:"key,label"`

		Validate *bool  `hcl:"validate"`
		Prefix   string `hcl:"prefix,optional"`

		KeyVals map[string]*hcl.Attribute `hcl:",remain"` // key value pairs to match on
	} `hcl:"jwt,block"`

	Posted map[string]string `hcl:"post_values,optional"`

	ServerName  interface{} // the context key to use for getting the server name
	HTTPRequest http.Request
}
