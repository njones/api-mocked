package main

//go:generate go build -buildmode=plugin -o ../../obj/faker.so

import (
	"fmt"
	"math/rand"
	plug "plugins/config"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/jaswdr/faker"
	"github.com/njones/logger"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

var _ plug.Plugin = &fakerPlugin{}

const fakerPluginName = "faker"

type fakerPlugin struct {
	data []fakeData
	ctx  *hcl.EvalContext
}

type fakeData struct {
	Prefix        string  `hcl:"prefix,label"`
	ProfileURL    *string `hcl:"profile_url"`
	ProfileMaxNum *int    `hcl:"profile_max"`
	Data          *struct {
		KVs map[string]*hcl.Attribute `hcl:",remain"`
	} `hcl:"data,block"`
	_kv map[string]string
}

var log = logger.New()

// SetupPluginExt ...
func SetupPluginExt() (string, interface{}) { return fakerPluginName, new(fakerPlugin) }

func (p *fakerPlugin) Setup() error {
	log.Println("[faker] setup plugin ...")
	return nil
}
func (p *fakerPlugin) Version(int32) int32 { return 1 }
func (p *fakerPlugin) Metadata() string {
	return `
metadata {
	version   = "0.1.0"
	author    = "Nika Jones"
	copyright = "Nika Jones - © 2021"
}
`
}
func (p *fakerPlugin) SetupConfig(string, hcl.Body) error {
	// set the default values
	for i, v := range p.data {
		if v.ProfileURL == nil {
			link := "https://randomuser.me/api/portraits/men/%d.jpg"
			p.data[i].ProfileURL = &link
		}
		if v.ProfileMaxNum == nil {
			max := 99
			p.data[i].ProfileMaxNum = &max
		}
	}
	return nil
}

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

			if blok.Data != nil {
			ReadBloks:
				for K, V := range blok.Data.KVs {
					v, _ := V.Expr.Value(p.fakeFunEvalContext(block, modeIn))
					switch v.Type() {
					case cty.String:
						blok._kv[K] = v.AsString()
					default:
						// special case looking up the profile_url
						// and adding the block name to the first parameter
						if fnce, ok := V.Expr.(*hclsyntax.FunctionCallExpr); ok {
							if fnc, ok := p.fakeFunEvalContext(block, modeIn).Functions[fnce.Name]; ok {
								for i, param := range fnc.Params() {
									switch param.Name {
									case "prefix_from_block":
										if i == 0 {
											strv, err := fnc.Call([]cty.Value{cty.StringVal(blok.Prefix)})
											if err != nil {
												panic(err) // TODO(njones): return error with std ErrXXXXX type
											}
											blok._kv[K] = strv.AsString()
											continue ReadBloks
										}
									}
								}
							}
						}
						panic(fmt.Errorf("a bad function (%s): %#v", K, v)) // TODO(njones): return error with std ErrXXXXX type
					}
				}
			}

			p.data = append(p.data, blok)
		}
	}

	p.ctx = p.HCLContext()

	return nil
}

func (p *fakerPlugin) Variables() map[string]cty.Value {
	return p.ctx.Variables
}

func (p *fakerPlugin) Functions() map[string]function.Function {
	return p.ctx.Functions
}

func (p *fakerPlugin) HCLContext() *hcl.EvalContext {
	if len(p.data) == 0 {
		return &hcl.EvalContext{}
	}

	ctx := p.fakeFunEvalContext(nil, modeEx)
	mBlok := make(map[string]cty.Value)
	for _, blok := range p.data {
		mKey := make(map[string]cty.Value)
		for k, v := range blok._kv {
			mKey[k] = cty.StringVal(v)
		}
		mBlok[blok.Prefix] = cty.ObjectVal(mKey)
	}
	ctx.Variables["faker"] = cty.ObjectVal(mBlok)
	return ctx
}

const (
	modeIn = iota + 1
	modeEx
)

func (p *fakerPlugin) fakeFunEvalContext(block *hcl.Block, mode int) *hcl.EvalContext {
	fake := faker.New()

	var FakeFun = func(fn func() string) function.Function {
		return function.New(&function.Spec{
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				return cty.StringVal(fn()), nil
			},
		})
	}

	var FakeFun1Int = func(fn func(int) string) function.Function {
		return function.New(&function.Spec{
			Params: []function.Parameter{
				{
					Name:             "max",
					Type:             cty.Number,
					AllowDynamicType: true,
					AllowMarked:      true,
				},
			},
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				num, _ := args[0].AsBigFloat().Int64()
				return cty.StringVal(fn(int(num))), nil
			},
		})
	}

	var FakeFun1Int2Str = func(fn func(int) []string) function.Function {
		return function.New(&function.Spec{
			Params: []function.Parameter{
				{
					Name:             "max",
					Type:             cty.Number,
					AllowDynamicType: true,
					AllowMarked:      true,
				},
				{
					Name:             "seperator",
					Type:             cty.String,
					AllowDynamicType: true,
					AllowMarked:      true,
				},
			},
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				num, _ := args[0].AsBigFloat().Int64()
				strs := fn(int(num))
				return cty.StringVal(strings.Join(strs, args[1].AsString())), nil
			},
		})
	}

	var FunInt2Str = func(fn func() int) func() string {
		return func() string { return fmt.Sprintf("%d", fn()) }
	}

	var FunF642Str = func(fn func() float64) func() string {
		return func() string { return fmt.Sprintf("%f.4", fn()) }
	}

	var ns string
	switch mode {
	case modeEx:
		ns = "faker_"
		if block != nil && len(block.Labels) > 0 {
			ns += block.Labels[0] + "_"
		}
	}

	var hec *hcl.EvalContext
	hec = &hcl.EvalContext{
		Variables: make(map[string]cty.Value),
		Functions: map[string]function.Function{
			ns + "name":     FakeFun(fake.Person().Name),
			ns + "address":  FakeFun(fake.Address().Address),
			ns + "city":     FakeFun(fake.Address().City),
			ns + "state":    FakeFun(fake.Address().State),
			ns + "postcode": FakeFun(fake.Address().PostCode),
			ns + "country":  FakeFun(fake.Address().Country),
			ns + "lat":      FakeFun(FunF642Str(fake.Address().Latitude)),
			ns + "long":     FakeFun(FunF642Str(fake.Address().Longitude)),

			ns + "app_name":    FakeFun(fake.App().Name),
			ns + "app_version": FakeFun(fake.App().Version),

			ns + "color_css":  FakeFun(fake.Color().CSS),
			ns + "color_name": FakeFun(fake.Color().ColorName),
			ns + "color_hex":  FakeFun(fake.Color().Hex),
			ns + "color_rgb":  FakeFun(fake.Color().RGB),

			ns + "email_company":       FakeFun(fake.Internet().CompanyEmail),
			ns + "domain":              FakeFun(fake.Internet().Domain),
			ns + "email":               FakeFun(fake.Internet().Email),
			ns + "email_free":          FakeFun(fake.Internet().FreeEmail),
			ns + "email_domain_free":   FakeFun(fake.Internet().FreeEmailDomain),
			ns + "http_method":         FakeFun(fake.Internet().HTTPMethod),
			ns + "ipv4":                FakeFun(fake.Internet().Ipv4),
			ns + "ipv6":                FakeFun(fake.Internet().Ipv6),
			ns + "ipv4_local":          FakeFun(fake.Internet().LocalIpv4),
			ns + "mac_address":         FakeFun(fake.Internet().MacAddress),
			ns + "password":            FakeFun(fake.Internet().Password),
			ns + "query":               FakeFun(fake.Internet().Query),
			ns + "email_safe":          FakeFun(fake.Internet().SafeEmail),
			ns + "email_domain_safe":   FakeFun(fake.Internet().SafeEmailDomain),
			ns + "slug":                FakeFun(fake.Internet().Slug),
			ns + "http_status_code":    FakeFun(FunInt2Str(fake.Internet().StatusCode)),
			ns + "http_status_message": FakeFun(fake.Internet().StatusCodeMessage),
			ns + "domain.tld":          FakeFun(fake.Internet().TLD),
			ns + "url":                 FakeFun(fake.Internet().URL),
			ns + "username":            FakeFun(fake.Internet().User),

			ns + "cc_exp":    FakeFun(fake.Payment().CreditCardExpirationDateString),
			ns + "cc_number": FakeFun(fake.Payment().CreditCardNumber),
			ns + "cc_type":   FakeFun(fake.Payment().CreditCardType),

			ns + "gender":        FakeFun(fake.Person().Gender),
			ns + "gender_female": FakeFun(fake.Person().GenderFemale),
			ns + "gender_male":   FakeFun(fake.Person().GenderMale),
			ns + "name_female":   FakeFun(fake.Person().NameFemale),
			ns + "name_male":     FakeFun(fake.Person().NameMale),
			ns + "ssn":           FakeFun(fake.Person().SSN),
			ns + "name_suffix":   FakeFun(fake.Person().Suffix),

			ns + "phone":          FakeFun(fake.Phone().Number),
			ns + "area_code":      FakeFun(fake.Phone().AreaCode),
			ns + "area_code_free": FakeFun(fake.Phone().TollFreeAreaCode),
			ns + "phone_free":     FakeFun(fake.Phone().ToolFreeNumber),

			ns + "ua_chrome":  FakeFun(fake.UserAgent().Chrome),
			ns + "ua_firefox": FakeFun(fake.UserAgent().Firefox),
			ns + "ua_ie":      FakeFun(fake.UserAgent().InternetExplorer),
			ns + "ua_opera":   FakeFun(fake.UserAgent().Opera),
			ns + "ua_safari":  FakeFun(fake.UserAgent().Safari),
			ns + "ua":         FakeFun(fake.UserAgent().UserAgent),

			ns + "profile_url": p.fakeProfilePicURL(block),

			ns + "lorem_paragraph": FakeFun1Int(fake.Lorem().Paragraph),
			ns + "lorem_sentence":  FakeFun1Int(fake.Lorem().Sentence),
			ns + "lorem_text":      FakeFun1Int(fake.Lorem().Text),
			ns + "lorem_word":      FakeFun(fake.Lorem().Word),
			ns + "lorem_words":     FakeFun1Int2Str(fake.Lorem().Words),

			ns + "uuid":          FakeFun(fake.UUID().V4),
			ns + "random_letter": FakeFun(fake.RandomLetter),
			ns + "random_number": FakeFun(FunInt2Str(fake.RandomDigit)),

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
					return cty.StringVal(fmt.Sprintf("NOT FOUND: %q", args[0].AsString())), fmt.Errorf("bad") // TODO(njones): return error with std ErrXXXXX type
				},
			}),
		},
	}

	// only allow concat to be used internal to faker blocks
	if mode == modeIn {
		hec.Functions["concat"] = function.New(&function.Spec{
			VarParam: &function.Parameter{
				Name:             "string",
				Type:             cty.String,
				AllowDynamicType: true,
				AllowMarked:      true,
			},
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
				sb := new(strings.Builder)
				for _, arg := range args {
					sb.WriteString(arg.AsString())
				}
				return cty.StringVal(sb.String()), nil
			},
		})
	}

	return hec
}

func (p *fakerPlugin) fakeProfilePicURL(block *hcl.Block) function.Function {
	return function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Params: []function.Parameter{
			{
				Name:             "prefix_from_block",
				Type:             cty.String,
				AllowDynamicType: false,
				AllowMarked:      false,
			},
		},
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			var blockName string
			switch {
			case len(args) > 0:
				blockName = args[0].AsString()
			case block != nil:
				blockName = block.Labels[0]
			default:
				n := rand.Intn(99)
				return cty.StringVal(fmt.Sprintf("https://randomuser.me/api/portraits/men/%d.jpg", n)), nil
			}
			for _, d := range p.data {
				if d.Prefix == blockName {
					n := rand.Intn(*d.ProfileMaxNum)
					return cty.StringVal(fmt.Sprintf(*d.ProfileURL, n)), nil
				}
			}
			// if the profile name is wrong or missing, then we just choose a random profile and don't fail
			n := rand.Intn(99)
			return cty.StringVal(fmt.Sprintf("https://randomuser.me/api/portraits/men/%d.jpg", n)), nil
		},
	})
}

func main() {}
