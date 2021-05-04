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
	requ "plugins/request"
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

// interval holds a map of the string names that
// can be used as interval times
var interval = map[string]time.Duration{
	"ns": time.Nanosecond,
	"ms": time.Millisecond,
	"µs": time.Microsecond,
	"us": time.Microsecond,
	"s":  time.Second,
	"m":  time.Minute,
	"h":  time.Hour,
}

// delay is the funtion used to return the
// time duration for delay formatted string
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

// reqStateFn is the recursive type that represents
// a state during the processing of a HTTP request
type reqStateFn func(*reqState) reqStateFn

// reqState is all of the state-wide values needed
// when processing a HTTP request
type reqState struct {
	state  reqStateFn
	status int

	req RequestHTTP
	res ResponseHTTP

	w http.ResponseWriter
	r *http.Request

	txts []TextBlock

	vars map[string]cty.Value         // HCL variables
	funs map[string]function.Function // HCL functions

	err error
}

// setup is the inital setup state where all things are
// initialized
func setup(idx *uint64, resps []ResponseHTTP, texts []TextBlock) reqStateFn {
	return func(st *reqState) reqStateFn {
		st.txts = texts
		return execOrder(idx, resps)
	}
}

// execOrder executes the Order of responses for each state
// this requires passing in the HTTP responses that can be used
// and the index as a reference, the index will be atomiclly
// incremented accross *all* requests. There currently is no
// way to increment for a single request profile  (ie user,
// instance, or some identifying factor)
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
		return execPrePluginRequestHTTP
	}
}

func execPrePluginRequestHTTP(st *reqState) reqStateFn {
	req := *st.r
	for _, plugin := range plugins {
		if plug, ok := plugin.(PrePluginRequestHTTP); ok {
			requHTTP := requ.HTTP{
				Method:      st.req.Method,
				Ticker:      st.req.Ticker,
				Order:       st.req.Order,
				Delay:       st.req.Delay,
				HTTPRequest: req,
				ServerName:  CtxKeyServerName,
			}
			if st.err = plug.PreRequestHTTP(st.req.Plugins, requHTTP); st.err != nil {
				return nil
			}
		}
	}
	return execDelay
}

// execDelay executed the delay of a request
func execDelay(st *reqState) reqStateFn {
	if len(st.req.Delay) > 0 {
		time.Sleep(delay(st.req.Delay))
	}
	return execStatus
}

// execStatus executes the return status of a request
// the status of a request must be a number, unless
// it's the name of a proxy server, in which case
// the request will be handed off to the proxy server
// and the status code will be determined by the
// proxy service
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

// useProxy returns the request via a proxy based on the
// configured proxy. It will take in any headers and send
// those in the request to the proxy server.
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

// execProxyHTTP executes a proxy server if the state requires it
func execProxyHTTP(resStatus string) reqStateFn {
	return func(st *reqState) reqStateFn {
		if proxy, ok := st.r.Context().Value(ctxKey(resStatus)).(*configProxy); ok {
			useProxy(st.w, st.r, proxy, st.res.Headers)
		}
		st.err = nil // exit smoothly regardless of past transgressions
		return nil
	}
}

// execAddVariables gathers all of the HIL variables that
// can be used in a HTTP request/response
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

// execAddFunctions gathers all of the HIL functions that can be
// used during the HTTP request/response
func execAddFunctions(funsCtx map[string]function.Function) reqStateFn {
	return func(st *reqState) reqStateFn {

		if _, ok := funsCtx["standard placeholder"]; !ok {
			return execFunCtxStandard(funsCtx)
		}

		if _, ok := funsCtx["plugin placeholder"]; !ok {
			return execFunCtxPlugin(funsCtx)
		}

		st.funs = funsCtx

		return execResponseHeaders
	}
}

// execVarCtxRequest executes gathering HIL Request variables
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

// execVarCtxHeader executes gathering HIL Request Header variables
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

// execVarCtxQuery executes gathering HIL Request Query variables
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

// execVarCtxPath executes gathering HIL variables from the URL path
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

// execVarCtxPost executes gathering HIL Request POST variables
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

// execVarCtxJWT executes gathering HIL JWT variables that were
// used during the request process
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

// execVarCtxPlugin executes gathering HIL variables that come from
// built-in or pre-build Go plugins
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

// execFunCtxStandard executes gathering standard HIL functions
func execFunCtxStandard(funsCtx map[string]function.Function) reqStateFn {
	return func(st *reqState) reqStateFn {
		type plugFns interface {
			Functions() map[string]function.Function
		}

		funsCtx["file"] = FileToStr("", "")

		funsCtx["text"] = TextBlockToStr(st.txts)

		funsCtx["standard placeholder"] = function.Function{} // a placeholder, standard functions have a different root
		return execAddFunctions(funsCtx)
	}
}

// execFunCtxPlugin executes gathering HIL functions from built-in or Go built plugins
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

		funsCtx["plugin placeholder"] = function.Function{} // a placeholder, plugins have a different root
		return execAddFunctions(funsCtx)
	}
}

// execResponseHeaders executes adding response headers to the response
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

// execOutput executes determining if the output is a JWT or some other output,
// which is currently a body
func execOutput(st *reqState) reqStateFn {
	if st.res.JWT != nil {
		return execJWTOutput
	}
	return execBodyOutput
}

// execJWTOutput executes gathering all of the JWT values for output
// this includes using the variable, and function contexts to determine
// the final output of values
func execJWTOutput(st *reqState) reqStateFn {
	var resJWT = st.res.JWT
	var cfgJWT, ok = st.r.Context().Value(ctxKey(resJWT.Name)).(*configJWT)
	if !ok {
		st.err = ErrJWTConfigurationNotFound
		return nil
	}

	resJWT._ctx = &hcl.EvalContext{Variables: st.vars, Functions: st.funs}

	var output, err = marshalJWT(cfgJWT, resJWT, st.r.Context().Value(CtxKeySignature))
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

// execBodyOutput exceutes determining if a body value
// needs to resolve variables and function calls
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

// execBodyTemplateOutput executes adding variables
// to the body to simulate a 0 index for variables
// that don't have an index but should.
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

// execBodyValueOutput resolves all of the function/variables
// within the HCL context
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

// finished writes an empty string to the output
func finished(st *reqState) reqStateFn { return finish("") }

// finish writes the out string to the output, with the status
// that was deterimed during the execStatus stage.
func finish(out string) reqStateFn {
	return func(st *reqState) reqStateFn {
		st.w.WriteHeader(int(st.status))
		fmt.Fprint(st.w, out)

		return nil
	}
}

// httpHandler returns the HTTP handler that can be added to the
// mux route, for a given path. This is what kicks off the
// state machine for every call. Pass in a req.rand Random number
// generater and you're own req.seed to make detereministic results
// for testing.
func httpHandler(req RequestHTTP, texts []TextBlock) http.HandlerFunc {
	var idx uint64
	if req.seed == 0 {
		req.seed = time.Now().UnixNano()
	}
	req.rand = rand.New(rand.NewSource(req.seed)) // doesn't have to be crypto-quality random here...
	resps := req.Response
	return WriteError(func(w http.ResponseWriter, r *http.Request) (err error) {
		st := &reqState{r: r, w: w, req: req}
		st.state = setup(&idx, resps, texts)
		for st.state != nil && st.err == nil {
			st.state = st.state(st)
		}
		return st.err
	})
}
