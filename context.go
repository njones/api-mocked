package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func _context() *hcl.EvalContext {
	var paramImpl = func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		str := args[0].AsString()
		if len(args) != 2 {
			str = strings.ToLower(str)
		}
		return cty.StringVal(fmt.Sprintf("{{ .%s }}", strings.Title(str))), nil
	}

	var ctx = &hcl.EvalContext{
		Functions: map[string]function.Function{
			"env": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "var",
						Type: cty.String,
					},
				},
				Type: function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
					return cty.StringVal(os.Getenv(args[0].AsString())), nil
				},
			}),
			"param": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "key",
						Type: cty.String,
					},
				},
				VarParam: &function.Parameter{
					Name: "raw",
					Type: cty.Bool,
				},
				Type: function.StaticReturnType(cty.String),
				Impl: paramImpl,
			}),
			"raw": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "key",
						Type: cty.String,
					},
				},
				Type: function.StaticReturnType(cty.String),
				Impl: paramImpl,
			}),
		},
	}

	return ctx
}

var fileEvalCtx = hcl.EvalContext{
	Variables: map[string]cty.Value{},
	Functions: map[string]function.Function{
		"file": FileToStr("file", "ctx"),
	},
}

var bodyEvalCtx = hcl.EvalContext{
	Variables: map[string]cty.Value{},
	Functions: map[string]function.Function{
		"file": FileToStr("body", "ctx"),
	},
}

func jwtEvalCtx(name, key string, vars map[string]map[string]cty.Value) *hcl.EvalContext {
	var ctx = &hcl.EvalContext{
		Variables: map[string]cty.Value{},
		Functions: map[string]function.Function{
			"now":      NowToStr,
			"file":     FileToStr(name, key),
			"unix":     UnixTsToStr,
			"duration": DurToStr,
		},
	}

	for k, v := range vars {
		ctx.Variables[k] = cty.ObjectVal(v)
	}

	return ctx
}

var DurToStr = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name: "duration",
			Type: cty.String,
		},
	},
	Type: function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		dur, err := time.ParseDuration(args[0].AsString())
		if err != nil {
			return cty.NumberIntVal(0), ErrParseDuration.F(err)
		}

		return cty.NumberIntVal(time.Now().Add(dur).Unix()), nil
	},
})

// fileToStrOpen allows the function to be overridden so we
// can add our own filesystem to open files, use the name, key
// to allow parallel tests based on those values.
var fileToStrOpen = func(_, _, filepath string) (io.ReadCloser, error) { return os.Open(filepath) }

func FileToStr(name string, key string) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name:             "filename",
				Type:             cty.String,
				AllowDynamicType: true,
				AllowMarked:      true,
			},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			// trim leading dots and slashes so we can't do some bad things.
			filepath := filepath.Join(_cfgFileLoadPath, strings.TrimLeft(args[0].AsString(), `.`+string(filepath.Separator)))
			f, err := fileToStrOpen(name, key, filepath)
			if err != nil {
				log.Fatal("expr file open:", err)
			}
			defer f.Close()
			b, err := ioutil.ReadAll(f)
			if err != nil {
				log.Fatal("expr file read:", err)
			}

			return cty.StringVal(string(bytes.TrimSpace(b))), nil
		},
	})
}

var NowToStr = function.New(&function.Spec{
	Params: []function.Parameter{},
	Type:   function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.NumberIntVal(time.Now().Unix()), nil
	},
})

var UnixTsToStr = function.New(&function.Spec{
	Params: []function.Parameter{
		{
			Name:             "unix",
			Type:             cty.String,
			AllowDynamicType: true,
			AllowMarked:      true,
		},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		a, err := time.Parse(time.RFC822, args[0].AsString())
		if err != nil {
			return cty.StringVal("0"), ErrParseTimeFmt.F(err)
		}
		return cty.StringVal(fmt.Sprintf("%d", a.Unix())), nil
	},
})
