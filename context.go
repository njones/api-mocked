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
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/json"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func decodeFile(filenames []string, ctx *hcl.EvalContext, target interface{}) error {
	var srcs = make([][]byte, len(filenames))

	for i, filename := range filenames {
		src, err := ioutil.ReadFile(filename)
		if err != nil {
			if os.IsNotExist(err) {
				return hcl.Diagnostics{
					{
						Severity: hcl.DiagError,
						Summary:  "Configuration file not found",
						Detail:   fmt.Sprintf("The configuration file %s does not exist.", filename),
					},
				}
			}
			return hcl.Diagnostics{
				{
					Severity: hcl.DiagError,
					Summary:  "Failed to read configuration",
					Detail:   fmt.Sprintf("Can't read %s: %s.", filename, err),
				},
			}
		}
		srcs[i] = src
	}

	return decode(filenames, srcs, ctx, target)
}

func decode(filenames []string, srcs [][]byte, ctx *hcl.EvalContext, target interface{}) error {
	var file *hcl.File
	var files []*hcl.File
	var diags hcl.Diagnostics

	for i, filename := range filenames {
		switch suffix := strings.ToLower(filepath.Ext(filename)); suffix {
		case ".hcl":
			file, diags = hclsyntax.ParseConfig(srcs[i], filename, hcl.Pos{Line: 1, Column: 1})
		case ".json":
			file, diags = json.Parse(srcs[i], filename)
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unsupported file format",
				Detail:   fmt.Sprintf("Cannot read from %s: unrecognized file format suffix %q.", filename, suffix),
			})
			return diags
		}
		if diags.HasErrors() {
			return diags
		}
		files = append(files, file)
	}

	diags = gohcl.DecodeBody(hcl.MergeFiles(files), ctx, target)
	if diags.HasErrors() {
		return diags
	}
	return nil
}

// _context returns the basic context that will be used to initially
// decode HCL documents.
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

// fileEvalCtx returns a context that should be used when
// a HCL Attribute can only use file functions
var fileEvalCtx = hcl.EvalContext{
	Variables: map[string]cty.Value{},
	Functions: map[string]function.Function{
		"file": FileToStr("file", "ctx"),
	},
}

// bodyEvalCtx returns a context that should be used
// for body strings. This gives them all of the same
// features for consiancy.
var bodyEvalCtx = hcl.EvalContext{
	Variables: map[string]cty.Value{},
	Functions: map[string]function.Function{
		"file": FileToStr("body", "ctx"),
	},
}

// jwtEvalCtx returns a context that should be used
// with JWT values that can have properties other
// than strings.
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

// TextBlockToStr takes in a textblock and return the
// data with args filled in
func TextBlockToStr(texts []TextBlock) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "name",
				Type: cty.String,
			},
		},
		VarParam: &function.Parameter{
			Type: cty.String,
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			var argVals = make(map[string]cty.Value)
			for i, argVal := range args {
				argVals[fmt.Sprintf("%d", i)] = argVal
			}
			ctx := &hcl.EvalContext{
				Variables: map[string]cty.Value{
					"arg": cty.ObjectVal(argVals),
				},
			}
			for _, txt := range texts {
				if txt.Name == args[0].AsString() {
					val, dia := txt.Data.Expr.Value(ctx)
					var err error
					if dia.HasErrors() {
						err = fmt.Errorf("text block: %v", dia)
					}
					switch val.Type() {
					case cty.Number:
						i, _ := val.AsBigFloat().Int64()
						val = cty.StringVal(fmt.Sprintf("%d", i))
					case cty.String:
						return val, err
					default:
						b, err := ctyjson.SimpleJSONValue{Value: val}.MarshalJSON()
						return cty.StringVal(string(b)), err
					}
				}
			}
			return cty.StringVal(""), nil
		},
	})
}

// DurToStr takes a tiem duration and returns a
// unix timestamp of the duration from time.Now()
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

// FileToStr takes in a name, key (used during testing) and returns a HCL
// function that will return the contents of a file as a string
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
			filepath := filepath.Join(_runtimePath, strings.TrimLeft(args[0].AsString(), `.`+string(filepath.Separator)))
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

// NowToStr returns the current time as a int64 unix time
var NowToStr = function.New(&function.Spec{
	Params: []function.Parameter{},
	Type:   function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.NumberIntVal(time.Now().Unix()), nil
	},
})

// UnixTsToStr takes in a RFC822 formatted string and
// returns the unix timestamp as a int64 number
//
// RFC822: Sat, 20 Feb 2021 00:00:00 UTC
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
