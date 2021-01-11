package main

// this file is from https://github.com/tmccombs/hcl2json/blob/master/convert/convert.go
// there are some modifications to allow using a _JSON_ heredoc as a json blob

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	ctyconvert "github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

type Options struct {
	Simplify bool
}

type converter struct {
	bytes   []byte
	options Options
	file    io.ReaderAt
}

type jsonObj map[string]interface{}

func (c *converter) convertBody(body *hclsyntax.Body) (out jsonObj, err error) {
	out = make(jsonObj)

	c.file, err = os.Open(body.SrcRange.Filename)
	if err != nil {
		log.Fatal("template file open:", err)
	}
	defer c.file.(*os.File).Close()

	for _, block := range body.Blocks {
		if err = c.convertBlock(block, out); err != nil {
			return nil, fmt.Errorf("convert block: %w", err)
		}
	}

	for key, value := range body.Attributes {
		out[key], err = c.convertExpression(value.Expr)
		if err != nil {
			return nil, fmt.Errorf("convert expression: %w", err)
		}
	}

	return out, nil
}

func (c *converter) rangeSource(r hcl.Range) string {
	// for some reason the range doesn't include the ending paren, so
	// check if the next character is an ending paren, and include it if it is.
	end := r.End.Byte
	if c.bytes[end] == ')' {
		end++
	}
	return string(c.bytes[r.Start.Byte:end])
}

func (c *converter) convertBlock(block *hclsyntax.Block, out jsonObj) error {
	key := block.Type
	for _, label := range block.Labels {

		// Labels represented in HCL are defined as quoted strings after the name of the block:
		// block "label_one" "label_two"
		//
		// Labels represtend in JSON are nested one after the other:
		// "label_one": {
		//   "label_two": {}
		// }
		//
		// To create the JSON representation, check to see if the label exists in the current output:
		//
		// When the label exists, move onto the next label reference.
		// When a label does not exist, create the label in the output and set that as the next label reference
		// in order to append (potential) labels to it.
		if _, exists := out[key]; exists {
			var ok bool
			out, ok = out[key].(jsonObj)
			if !ok {
				return fmt.Errorf("Unable to convert Block to JSON: %v.%v", block.Type, strings.Join(block.Labels, "."))
			}
		} else {
			out[key] = make(jsonObj)
			out = out[key].(jsonObj)
		}

		key = label
	}

	value, err := c.convertBody(block.Body)
	if err != nil {
		return fmt.Errorf("convert body: %w", err)
	}

	// Multiple blocks can exist with the same name, at the same
	// level in the JSON document (e.g. locals).
	//
	// For consistency, always wrap the value in a collection.
	// When multiple values are at the same key
	if current, exists := out[key]; exists {
		out[key] = append(current.([]interface{}), value)
	} else {
		out[key] = []interface{}{value}
	}

	return nil
}

func (c *converter) convertExpression(expr hclsyntax.Expression) (interface{}, error) {
	if c.options.Simplify {
		value, err := expr.Value(&evalContext)
		if err == nil {
			return ctyjson.SimpleJSONValue{Value: value}, nil
		}
	}

	// assume it is hcl syntax (because, um, it is)
	switch value := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		return ctyjson.SimpleJSONValue{Value: value.Val}, nil
	case *hclsyntax.UnaryOpExpr:
		return c.convertUnary(value)
	case *hclsyntax.TemplateExpr:
		return c.convertTemplate(value)
	case *hclsyntax.TemplateWrapExpr:
		return c.convertExpression(value.Wrapped)
	case *hclsyntax.TupleConsExpr:
		list := make([]interface{}, 0)
		for _, ex := range value.Exprs {
			elem, err := c.convertExpression(ex)
			if err != nil {
				return nil, err
			}
			list = append(list, elem)
		}
		return list, nil
	case *hclsyntax.ObjectConsExpr:
		m := make(jsonObj)
		for _, item := range value.Items {
			key, err := c.convertKey(item.KeyExpr)
			if err != nil {
				return nil, err
			}
			m[key], err = c.convertExpression(item.ValueExpr)
			if err != nil {
				return nil, err
			}
		}
		return m, nil
	default:
		return c.wrapExpr(expr), nil
	}
}

func (c *converter) convertUnary(v *hclsyntax.UnaryOpExpr) (interface{}, error) {
	_, isLiteral := v.Val.(*hclsyntax.LiteralValueExpr)
	if !isLiteral {
		// If the expression after the operator isn't a literal, fall back to
		// wrapping the expression with ${...}
		return c.wrapExpr(v), nil
	}
	val, err := v.Value(nil)
	if err != nil {
		return nil, err
	}
	return ctyjson.SimpleJSONValue{Value: val}, nil
}

func (c *converter) convertTemplate(t *hclsyntax.TemplateExpr) (interface{}, error) {
	if t.IsStringLiteral() {
		// safe because the value is just the string
		v, err := t.Value(nil)
		if err != nil {
			return "", err
		}

		b := make([]byte, t.SrcRange.End.Column)
		c.file.ReadAt(b, int64(t.SrcRange.End.Byte-t.SrcRange.End.Column))

		if strings.TrimSpace(string(b)) == "_JSON_" {
			m := make(map[string]interface{})
			err := json.Unmarshal([]byte(v.AsString()), &m)
			return m, err
		}

		return v.AsString(), nil
	}

	var builder strings.Builder
	for _, part := range t.Parts {
		s, err := c.convertStringPart(part)
		if err != nil {
			return "", err
		}
		builder.WriteString(s)
	}

	return builder.String(), nil
}

func (c *converter) convertStringPart(expr hclsyntax.Expression) (string, error) {
	switch v := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		s, err := ctyconvert.Convert(v.Val, cty.String)
		if err != nil {
			return "", err
		}
		return s.AsString(), nil
	case *hclsyntax.TemplateExpr:
		str, err := c.convertTemplate(v)
		return str.(string), err
	case *hclsyntax.TemplateWrapExpr:
		return c.convertStringPart(v.Wrapped)
	case *hclsyntax.ConditionalExpr:
		return c.convertTemplateConditional(v)
	case *hclsyntax.TemplateJoinExpr:
		return c.convertTemplateFor(v.Tuple.(*hclsyntax.ForExpr))
	default:
		// treating as an embedded expression
		return c.wrapExpr(expr), nil
	}
}

func (c *converter) convertKey(keyExpr hclsyntax.Expression) (string, error) {
	// a key should never have dynamic input
	if k, isKeyExpr := keyExpr.(*hclsyntax.ObjectConsKeyExpr); isKeyExpr {
		keyExpr = k.Wrapped
		if _, isTraversal := keyExpr.(*hclsyntax.ScopeTraversalExpr); isTraversal {
			return c.rangeSource(keyExpr.Range()), nil
		}
	}
	return c.convertStringPart(keyExpr)
}

func (c *converter) convertTemplateConditional(expr *hclsyntax.ConditionalExpr) (string, error) {
	var builder strings.Builder
	builder.WriteString("%{if ")
	builder.WriteString(c.rangeSource(expr.Condition.Range()))
	builder.WriteString("}")
	trueResult, err := c.convertStringPart(expr.TrueResult)
	if err != nil {
		return "", nil
	}
	builder.WriteString(trueResult)
	falseResult, err := c.convertStringPart(expr.FalseResult)
	if len(falseResult) > 0 {
		builder.WriteString("%{else}")
		builder.WriteString(falseResult)
	}
	builder.WriteString("%{endif}")

	return builder.String(), nil
}

func (c *converter) convertTemplateFor(expr *hclsyntax.ForExpr) (string, error) {
	var builder strings.Builder
	builder.WriteString("%{for ")
	if len(expr.KeyVar) > 0 {
		builder.WriteString(expr.KeyVar)
		builder.WriteString(", ")
	}
	builder.WriteString(expr.ValVar)
	builder.WriteString(" in ")
	builder.WriteString(c.rangeSource(expr.CollExpr.Range()))
	builder.WriteString("}")
	templ, err := c.convertStringPart(expr.ValExpr)
	if err != nil {
		return "", err
	}
	builder.WriteString(templ)
	builder.WriteString("%{endfor}")

	return builder.String(), nil
}

func (c *converter) wrapExpr(expr hclsyntax.Expression) string {
	return "${" + c.rangeSource(expr.Range()) + "}"
}

var jwtContext = hcl.EvalContext{
	Functions: map[string]function.Function{
		"now":      NowStr,
		"file":     FileStr,
		"unix":     UnixStr,
		"duration": DurStr,
	},
}

var pnContext = hcl.EvalContext{
	Functions: map[string]function.Function{
		"file": FileStr,
	},
}

var bodyContext = hcl.EvalContext{
	Functions: map[string]function.Function{
		"file": FileStr,
	},
}

var DurStr = function.New(&function.Spec{
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
			return cty.NumberIntVal(0), fmt.Errorf("couldn't parse time duration")
		}

		return cty.NumberIntVal(time.Now().Add(dur).Unix()), nil
	},
})

var UnixStr = function.New(&function.Spec{
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
		if a, err := time.Parse(time.RFC822, args[0].AsString()); err == nil {
			return cty.StringVal(fmt.Sprintf("%d", a.Unix())), nil
		}
		return cty.StringVal("0"), fmt.Errorf("couldn't find time parser")
	},
})

var NowStr = function.New(&function.Spec{
	Params: []function.Parameter{},
	Type:   function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.NumberIntVal(time.Now().Unix()), nil
	},
})

var FileStr = function.New(&function.Spec{
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
		f, err := os.Open(filepath)
		if err != nil {
			log.Fatal("file bin open err:", err)
		}
		defer f.Close()
		b, err := ioutil.ReadAll(f)
		if err != nil {
			log.Fatal("file bin read err:", err)
		}

		return cty.StringVal(string(b)), nil
	},
})

// a subset of functions used in terraform
// that can be used when simplifying during conversion
var evalContext = hcl.EvalContext{
	Functions: map[string]function.Function{
		// numeric
		// "abs": stdlib.AbsoluteFunc,
		// "ceil":     stdlib.CeilFunc,
		// "floor":    stdlib.FloorFunc,
		// "log":      stdlib.LogFunc,
		"max": stdlib.MaxFunc,
		"min": stdlib.MinFunc,
		// "parseint": stdlib.ParseIntFunc,
		// "pow":      stdlib.PowFunc,
		// "signum":   stdlib.SignumFunc,

		// string
		// "chomp":      stdlib.ChompFunc,
		"format":     stdlib.FormatFunc,
		"formatlist": stdlib.FormatListFunc,
		// "indent":     stdlib.IndentFunc,
		// "join":       stdlib.JoinFunc,
		// "split":      stdlib.SplitFunc,
		// "strrev": stdlib.ReverseFunc,
		// "trim":       stdlib.TrimFunc,
		// "trimprefix": stdlib.TrimPrefixFunc,
		// "trimsuffix": stdlib.TrimSuffixFunc,
		// "trimspace":  stdlib.TrimSpaceFunc,

		// collections
		// "chunklist": stdlib.ChunklistFunc,
		"concat": stdlib.ConcatFunc,
		// "distinct":  stdlib.DistinctFunc,
		// "flatten":   stdlib.FlattenFunc,
		"length": stdlib.LengthFunc,
		// "merge":     stdlib.MergeFunc,
		// "reverse":   stdlib.ReverseListFunc,
		// "sort":      stdlib.SortFunc,

		// encoding
		"csvdecode":  stdlib.CSVDecodeFunc,
		"jsondecode": stdlib.JSONDecodeFunc,
		"jsonencode": stdlib.JSONEncodeFunc,

		// time
		"formatdate": stdlib.FormatDateFunc,
		// "timeadd":    stdlib.TimeAddFunc,
	},
}
