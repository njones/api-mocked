package main

//go:generate go build -buildmode=plugin -o ../../obj/prettyprintjson.so

import (
	plug "plugins/config"

	"github.com/hashicorp/hcl/v2"
	"github.com/njones/logger"
	"github.com/tidwall/pretty"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

var _ plug.Plugin = &prettyPrintJSONPlugin{}

const prettyPrintJSONPluginName = "pretty_print_json"

type prettyPrintJSONPlugin struct{}

var log = logger.New()

func (p *prettyPrintJSONPlugin) WithLogger(l logger.Logger) {
	log = l
}

// SetupPluginExt ...
func SetupPluginExt() (string, interface{}) {
	return prettyPrintJSONPluginName, new(prettyPrintJSONPlugin)
}

func (p *prettyPrintJSONPlugin) Setup() error {
	log.Println("[pretty_print JSON] setup plugin ...")
	return nil
}
func (p *prettyPrintJSONPlugin) Version(i int32) int32 { return i }
func (p *prettyPrintJSONPlugin) Metadata() string {
	return `
metadata {
	version   = "0.1.0"
	author    = "Nika Jones"
	copyright = "Nika Jones - © 2021"
}
`
}
func (p *prettyPrintJSONPlugin) SetupConfig(string, hcl.Body) error { return nil }

func (p *prettyPrintJSONPlugin) SetupRoot(plugins hcl.Body) error { return nil }

func (p *prettyPrintJSONPlugin) Functions() map[string]function.Function {
	return map[string]function.Function{
		"pretty_print_json": function.New(&function.Spec{
			Params: []function.Parameter{
				{
					Name:             "json",
					Type:             cty.String,
					AllowDynamicType: true,
					AllowMarked:      true,
				},
			},
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				prettyJSON := pretty.Pretty([]byte(args[0].AsString()))
				return cty.StringVal(string(prettyJSON)), nil
			},
		}),
	}
}

func main() {}
