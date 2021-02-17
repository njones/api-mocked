package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

type responseWriter struct {
	wrote  bool
	status int
	http.ResponseWriter
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	rw.wrote = true
	return rw.ResponseWriter.Write(p)
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	return rw.ResponseWriter.(http.Pusher).Push(target, opts)
}

var interval = map[string]time.Duration{
	"ns": time.Nanosecond,
	"ms": time.Millisecond,
	"µs": time.Microsecond,
	"s":  time.Second,
	"m":  time.Minute,
	"h":  time.Hour,
}

func delay(str string) time.Duration {
	var n int
	var i string
	if x, _ := fmt.Sscanf(str, "%d%s", &n, &i); x > 0 {
		if d, ok := interval[i]; ok {
			return time.Duration(n) * d
		}
	}

	return time.Duration(0)
}

type reqStateFn func(*reqState) reqStateFn

type reqState struct {
	state  reqStateFn
	status int

	req RequestHTTP
	res ResponseHTTP

	w http.ResponseWriter
	r *http.Request

	vars map[string]cty.Value         // HCL variables
	funs map[string]function.Function // HCL functions

	err error
}

func setup(idx *uint64, resps []ResponseHTTP) reqStateFn {
	return func(st *reqState) reqStateFn {
		// where all things  are init'd
		return execOrder(idx, resps)
	}
}

func execOrder(idx *uint64, resps []ResponseHTTP) reqStateFn {
	return func(st *reqState) reqStateFn {
		var order uint64
		switch st.req.Order {
		case "random":
			order = uint64(st.req.rand.Int63n(int64(len(resps) * 2)))
		case "unordered":
			order = atomic.AddUint64(idx, 1) - 1
			if int(order)%len(resps) == 0 {
				st.req.rand.Shuffle(len(resps), func(i, j int) { resps[i], resps[j] = resps[j], resps[i] })
			}
		default:
			order = atomic.AddUint64(idx, 1) - 1
		}
		st.res = resps[int(order)%len(resps)]
		return execDelay
	}
}

func execDelay(st *reqState) reqStateFn {
	if len(st.req.Delay) > 0 {
		time.Sleep(delay(st.req.Delay))
	}
	return execStatus
}

func execStatus(st *reqState) reqStateFn {
	var resStatus = st.res.Status
	if resStatus == "" {
		resStatus = "200"
	}

	st.status, st.err = strconv.Atoi(resStatus)
	if st.err != nil {
		var numError *strconv.NumError
		if errors.As(st.err, &numError) { // then we're usually looking at words
			st.err = nil // clear error before the next state
			return execProxyHTTP(resStatus)
		}
		st.err = ErrOrderIndexParse.F(st.err)
		return nil // display error
	}

	varsCtx := make(map[string]cty.Value)
	return execAddVariables(varsCtx)
}

func useProxy(w http.ResponseWriter, r *http.Request, proxy *configProxy, headers *headers) {
	xy := httputil.NewSingleHostReverseProxy(proxy._url)

	r.Host = proxy._url.Host
	r.URL.Host = proxy._url.Host

	if headers != nil {
		for k, vals := range headers.Data {
			for _, val := range vals {
				r.Header.Set(k, val.AsString())
			}
		}
	}
	if proxy.Headers != nil {
		for k, vals := range proxy.Headers.Data {
			for _, val := range vals {
				r.Header.Set(k, val.AsString())
			}
		}
	}

	r.URL.Scheme = proxy._url.Scheme
	log.Printf("[http] [proxy] to %s", proxy._url.String())
	xy.ServeHTTP(w, r)
}

func execProxyHTTP(resStatus string) reqStateFn {
	return func(st *reqState) reqStateFn {
		if proxy, ok := st.r.Context().Value(ctxKey(resStatus)).(*configProxy); ok {
			useProxy(st.w, st.r, proxy, st.res.Headers)
		}
		st.err = nil // exit smoothly regardless of past transgressions
		return nil
	}
}

func execAddVariables(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {

		if _, ok := varsCtx["request"]; !ok {
			return execVarCtxRequest(varsCtx)
		}
		if _, ok := varsCtx["header"]; !ok {
			return execVarCtxHeader(varsCtx)
		}
		if _, ok := varsCtx["query"]; !ok {
			return execVarCtxQuery(varsCtx)
		}
		if _, ok := varsCtx["url"]; !ok {
			return execVarCtxPath(varsCtx)
		}
		if _, ok := varsCtx["post"]; !ok {
			return execVarCtxPost(varsCtx)
		}
		if _, ok := varsCtx["jwt"]; !ok {
			return execVarCtxJWT(varsCtx)
		}
		if _, ok := varsCtx["plugin"]; !ok {
			return execVarCtxPlugin(varsCtx)
		}

		st.vars = varsCtx

		funsCtx := make(map[string]function.Function)
		return execAddFunctions(funsCtx)
	}
}

func execAddFunctions(funsCtx map[string]function.Function) reqStateFn {
	return func(st *reqState) reqStateFn {

		if _, ok := funsCtx["plugin"]; !ok {
			return execFunCtxPlugin(funsCtx)
		}

		st.funs = funsCtx

		return execResponseHeaders
	}
}

func execVarCtxRequest(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		if st.r.Method != http.MethodPost {
			varsCtx["request"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		body, err := ioutil.ReadAll(io.LimitReader(st.r.Body, (2^20)*10)) // 10MB limit
		if err != nil {
			st.err = ErrReadRequestBody.F(err)
			return nil
		}
		requestCtx := map[string]cty.Value{
			"body": cty.StringVal(string(body)),
		}

		varsCtx["request"] = cty.ObjectVal(requestCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxHeader(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		params := st.r.Header
		if len(params) == 0 {
			varsCtx["header"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		headerCtx := make(map[string]cty.Value)
		for k, vals := range params {
			indexCtx := make(map[string]cty.Value)
			for i, val := range vals {
				indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
			}
			k = strings.ToLower(k)
			headerCtx[k] = cty.ObjectVal(indexCtx)
		}

		varsCtx["header"] = cty.ObjectVal(headerCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxQuery(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		params := st.r.URL.Query()
		if len(params) == 0 {
			varsCtx["query"] = cty.NilVal
			execAddVariables(varsCtx)
		}

		queryCtx := make(map[string]cty.Value)
		for k, vals := range params {
			indexCtx := make(map[string]cty.Value)
			for i, val := range vals {
				indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
			}
			k = strings.ToLower(k)
			queryCtx[k] = cty.ObjectVal(indexCtx)
		}

		varsCtx["query"] = cty.ObjectVal(queryCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxPath(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		params := chi.RouteContext(st.r.Context()).URLParams
		if len(params.Keys) == 0 {
			varsCtx["url"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		urlCtx := make(map[string]cty.Value)
		for i, k := range params.Keys {
			k = strings.ToLower(k)
			urlCtx[k] = cty.StringVal(params.Values[i])
		}

		varsCtx["url"] = cty.ObjectVal(urlCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxPost(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		params := st.r.Form
		if params == nil || len(params) == 0 {
			varsCtx["post"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		postCtx := make(map[string]cty.Value)
		for k, vals := range params {
			indexCtx := make(map[string]cty.Value)
			for i, val := range vals {
				indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
			}
			k = strings.ToLower(k)
			postCtx[k] = cty.ObjectVal(indexCtx)
		}

		varsCtx["post"] = cty.ObjectVal(postCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxJWT(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		token, ok := st.r.Context().Value(CtxKeyJWTToken).(*jwtgo.Token)
		if !ok {
			varsCtx["jwt"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		params, ok := token.Claims.(jwtgo.MapClaims)
		if !ok {
			varsCtx["jwt"] = cty.NilVal
			return execAddVariables(varsCtx)
		}

		jwtCtx := make(map[string]cty.Value)
		for k, val := range params {
			if v, ok := val.(string); ok {
				jwtCtx[k] = cty.StringVal(v)
			}
		}

		varsCtx["jwt"] = cty.ObjectVal(jwtCtx)
		return execAddVariables(varsCtx)
	}
}

func execVarCtxPlugin(varsCtx map[string]cty.Value) reqStateFn {
	return func(st *reqState) reqStateFn {
		type plugVars interface {
			Variables() map[string]cty.Value
		}

		for _, plugin := range plugins {
			if plug, ok := plugin.(plugVars); ok {
				for k, v := range plug.Variables() {
					varsCtx[k] = v // set the plugin name to the root of the context directly
				}
			}
		}

		varsCtx["plugin"] = cty.NilVal // a placeholder, the plugins have a different root
		return execAddVariables(varsCtx)
	}
}

func execFunCtxPlugin(funsCtx map[string]function.Function) reqStateFn {
	return func(st *reqState) reqStateFn {
		type plugFns interface {
			Functions() map[string]function.Function
		}

		for _, plugin := range plugins {
			if plug, ok := plugin.(plugFns); ok {
				for k, v := range plug.Functions() {
					funsCtx[k] = v // set the plugin name to the root of the context directly
				}
			}
		}

		funsCtx["plugin"] = function.Function{} // a placeholder, plugins have a different root
		return execAddFunctions(funsCtx)
	}
}

func execResponseHeaders(st *reqState) reqStateFn {
	if st.res.Headers == nil {
		return execOutput
	}

	for k, vals := range st.res.Headers.Data {
		for _, val := range vals {
			st.w.Header().Add(k, val.AsString())
		}
	}
	return execOutput
}

func execOutput(st *reqState) reqStateFn {
	if st.res.JWT != nil {
		return execJWTOutput
	}
	return execBodyOutput
}

func execJWTOutput(st *reqState) reqStateFn {
	var resJWT = st.res.JWT
	var cfgJWT, ok = st.r.Context().Value(ctxKey(resJWT.Name)).(*configJWT)
	if !ok {
		st.err = ErrJWTConfigurationNotFound
		return nil
	}

	resJWT._ctx = &hcl.EvalContext{Variables: st.vars, Functions: st.funs}

	var output, err = marshalJWT(cfgJWT, resJWT, st.r.Context().Value(sigCtxKey))
	if err != nil {
		st.err = ErrMarshalJWT.F(err)
		return nil
	}

	switch resJWT.Output {
	case "header":
		st.w.Header().Add(resJWT.Key, output)
		return finished
	case "cookie":
		http.SetCookie(st.w, &http.Cookie{
			Name:  resJWT.Key,
			Value: output,
		})
		return finished
	}

	return finish(output)
}

func execBodyOutput(st *reqState) reqStateFn {
	if st.res.Body == nil {
		return finished
	}

	switch st.res.Body.Expr.(type) {
	case *hclsyntax.TemplateExpr:
		return execBodyTemplateOutput
	}

	return execBodyValueOutput
}

func execBodyTemplateOutput(st *reqState) reqStateFn {
	body, _ := st.res.Body.Expr.(*hclsyntax.TemplateExpr)

LookForIndexes:
	for i, part := range body.Parts {
		variables := part.Variables()
		for _, vars := range variables {
			for _, v := range vars {
				if root, ok := v.(hcl.TraverseRoot); ok {
					switch root.Name {
					case "header", "query", "post":
						continue
					}
					break LookForIndexes
				}
				if _, ok := v.(hcl.TraverseIndex); ok {
					break LookForIndexes
				}
			}

			// append the index to header, query and post values that don't have one.
			st.
				res.Body.
				Expr.(*hclsyntax.TemplateExpr).
				Parts[i].(*hclsyntax.ScopeTraversalExpr).
				Traversal = append(st.
				res.Body.
				Expr.(*hclsyntax.TemplateExpr).
				Parts[i].(*hclsyntax.ScopeTraversalExpr).
				Traversal, hcl.TraverseIndex{
				SrcRange: hcl.Range{
					Filename: "internal",
				},
				Key: cty.NumberIntVal(0),
			})
		}
	}
	return execBodyValueOutput
}

func execBodyValueOutput(st *reqState) reqStateFn {
	ctx := &hcl.EvalContext{Variables: st.vars, Functions: st.funs}

	expr, dia := st.res.Body.Expr.Value(ctx)
	if dia.HasErrors() {
		st.err = ErrBadHCLExpression.F400(dia)
		return nil
	}

	if expr.Type() == cty.String {
		return finish(expr.AsString())
	}

	b, err := json.Marshal(ctyjson.SimpleJSONValue{Value: expr})
	if err != nil {
		st.err = ErrBadHCLExpression.F400(err)
		return nil
	}

	return finish(string(b))
}

func finished(st *reqState) reqStateFn { return finish("") }

func finish(out string) reqStateFn {
	return func(st *reqState) reqStateFn {
		st.w.WriteHeader(int(st.status))
		fmt.Fprint(st.w, out)

		return nil
	}
}

func httpHandler(req RequestHTTP) http.HandlerFunc {
	var idx uint64
	if req.seed == 0 {
		req.seed = time.Now().UnixNano()
	}
	req.rand = rand.New(rand.NewSource(req.seed)) // doesn't have to be crypto-quality random here...
	resps := req.Response
	return WriteError(func(w http.ResponseWriter, r *http.Request) (err error) {
		st := &reqState{r: r, w: w, req: req}
		st.state = setup(&idx, resps)
		for st.state != nil && st.err == nil {
			st.state = st.state(st)
		}
		return st.err
	})
}
