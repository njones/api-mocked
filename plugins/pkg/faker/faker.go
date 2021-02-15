package main

//go:generate go build -buildmode=plugin -o ../../obj/faker.so

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/jaswdr/faker"
	"github.com/njones/logger"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

const fakerPluginName = "faker"

type fakerPlugin struct {
	data []fakeData
}

type fakeData struct {
	Prefix string                    `hcl:"prefix,label"`
	KVs    map[string]*hcl.Attribute `hcl:",remain"`
	_kv    map[string]string
}

var log = logger.New()

func SetupPluginExt() (string, interface{}) { return fakerPluginName, new(fakerPlugin) }

func (p *fakerPlugin) Setup() error {
	log.Println("[faker] setup plugin ...")
	return nil
}
func (p *fakerPlugin) SetupConfig(string, hcl.Body) error { return nil }

func (p *fakerPlugin) WithLogger(l logger.Logger) {
	log = l
}

func (p *fakerPlugin) SetupRoot(plugins hcl.Body) error {
	log.Println("[faker] block checks ...")

	cfgb, _, _ := plugins.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       fakerPluginName,
				LabelNames: []string{"prefix"},
			},
		},
	})

	for _, block := range cfgb.Blocks {
		var blok fakeData
		blok._kv = make(map[string]string)
		switch block.Type {
		case fakerPluginName:
			gohcl.DecodeBody(block.Body, nil, &blok)
			if len(block.Labels) > 0 {
				blok.Prefix = block.Labels[0] // the same index as the LabelNames above...
			}

			for K, V := range blok.KVs {
				v, _ := V.Expr.Value(fakeFunEvalContext())
				blok._kv[K] = v.AsString()
			}

			p.data = append(p.data, blok)
		}
	}

	return nil
}

func (p *fakerPlugin) Variables() map[string]cty.Value {
	return p.HCLContext().Variables
}

func (p *fakerPlugin) Functions() map[string]function.Function {
	return p.HCLContext().Functions
}

func (p *fakerPlugin) HCLContext() *hcl.EvalContext {
	if len(p.data) == 0 {
		return &hcl.EvalContext{}
	}

	ctx := fakeFunEvalContext()
	mBlok := make(map[string]cty.Value)
	for _, blok := range p.data {
		mKey := make(map[string]cty.Value)
		for k, v := range blok._kv {
			mKey[k] = cty.StringVal(v)
		}
		mBlok[blok.Prefix] = cty.ObjectVal(mKey)
	}
	ctx.Variables["fake"] = cty.ObjectVal(mBlok)
	return ctx
}

func fakeFunEvalContext() *hcl.EvalContext {
	fake := faker.New()

	var FakeFun = func(fn func() string) function.Function {
		return function.New(&function.Spec{
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				return cty.StringVal(fn()), nil
			},
		})
	}

	var FunInt2Str = func(fn func() int) func() string {
		return func() string { return fmt.Sprintf("%d", fn()) }
	}

	var FunF642Str = func(fn func() float64) func() string {
		return func() string { return fmt.Sprintf("%f.4", fn()) }
	}

	var hec *hcl.EvalContext
	hec = &hcl.EvalContext{
		Variables: make(map[string]cty.Value),
		Functions: map[string]function.Function{
			"name":     FakeFun(fake.Person().Name),
			"address":  FakeFun(fake.Address().Address),
			"city":     FakeFun(fake.Address().City),
			"state":    FakeFun(fake.Address().State),
			"postcode": FakeFun(fake.Address().PostCode),
			"country":  FakeFun(fake.Address().Country),
			"lat":      FakeFun(FunF642Str(fake.Address().Latitude)),
			"long":     FakeFun(FunF642Str(fake.Address().Longitude)),

			"app_name":    FakeFun(fake.App().Name),
			"app_version": FakeFun(fake.App().Version),

			"color_css":  FakeFun(fake.Color().CSS),
			"color_name": FakeFun(fake.Color().ColorName),
			"color_hex":  FakeFun(fake.Color().Hex),
			"color_rgb":  FakeFun(fake.Color().RGB),

			"email_company":       FakeFun(fake.Internet().CompanyEmail),
			"domain":              FakeFun(fake.Internet().Domain),
			"email":               FakeFun(fake.Internet().Email),
			"email_free":          FakeFun(fake.Internet().FreeEmail),
			"email_domain_free":   FakeFun(fake.Internet().FreeEmailDomain),
			"http_method":         FakeFun(fake.Internet().HTTPMethod),
			"ipv4":                FakeFun(fake.Internet().Ipv4),
			"ipv6":                FakeFun(fake.Internet().Ipv6),
			"ipv4_local":          FakeFun(fake.Internet().LocalIpv4),
			"mac_address":         FakeFun(fake.Internet().MacAddress),
			"password":            FakeFun(fake.Internet().Password),
			"query":               FakeFun(fake.Internet().Query),
			"email_safe":          FakeFun(fake.Internet().SafeEmail),
			"email_domain_safe":   FakeFun(fake.Internet().SafeEmailDomain),
			"slug":                FakeFun(fake.Internet().Slug),
			"http_status_code":    FakeFun(FunInt2Str(fake.Internet().StatusCode)),
			"http_status_message": FakeFun(fake.Internet().StatusCodeMessage),
			"domain.tld":          FakeFun(fake.Internet().TLD),
			"url":                 FakeFun(fake.Internet().URL),
			"username":            FakeFun(fake.Internet().User),

			"cc_exp":    FakeFun(fake.Payment().CreditCardExpirationDateString),
			"cc_number": FakeFun(fake.Payment().CreditCardNumber),
			"cc_type":   FakeFun(fake.Payment().CreditCardType),

			"gender":        FakeFun(fake.Person().Gender),
			"gender_female": FakeFun(fake.Person().GenderFemale),
			"gender_male":   FakeFun(fake.Person().GenderMale),
			"name_female":   FakeFun(fake.Person().NameFemale),
			"name_male":     FakeFun(fake.Person().NameMale),
			"ssn":           FakeFun(fake.Person().SSN),
			"name_suffix":   FakeFun(fake.Person().Suffix),

			"phone":          FakeFun(fake.Phone().Number),
			"area_code":      FakeFun(fake.Phone().AreaCode),
			"area_code_free": FakeFun(fake.Phone().TollFreeAreaCode),
			"phone_free":     FakeFun(fake.Phone().ToolFreeNumber),

			"ua_chrome":  FakeFun(fake.UserAgent().Chrome),
			"ua_firefox": FakeFun(fake.UserAgent().Firefox),
			"ua_ie":      FakeFun(fake.UserAgent().InternetExplorer),
			"ua_opera":   FakeFun(fake.UserAgent().Opera),
			"ua_safari":  FakeFun(fake.UserAgent().Safari),
			"ua":         FakeFun(fake.UserAgent().UserAgent),

			"faker": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name:             "fake_kind",
						Type:             cty.String,
						AllowDynamicType: true,
						AllowMarked:      true,
					},
				},
				Type: function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
					str := args[0].AsString()
					str = strings.ReplaceAll(str, ".", "_")
					if fn, ok := hec.Functions[str]; ok {
						return fn.Call([]cty.Value{})
					}
					return cty.StringVal(fmt.Sprintf("NOT FOUND: %q", args[0].AsString())), fmt.Errorf("bad")
				},
			}),
		},
	}

	return hec
}

func main() {}
