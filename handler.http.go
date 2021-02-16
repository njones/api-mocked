package main

import (
	"encoding/json"
	"fmt"
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

func respHTTPContext(mV map[string]cty.Value, mF map[string]function.Function) *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: mV,
		Functions: mF,
	}
}

func respOrderHTTP(idx *uint64, resps []ResponseHTTP, orderType string) (ResponseHTTP, bool) {
	if len(resps) == 0 {
		return ResponseHTTP{}, false
	}
	var x uint64
	switch orderType {
	case "random":
		x = uint64(rand.Int63n(int64(len(resps) * 2)))
	case "unordered":
		x = atomic.AddUint64(idx, 1) - 1
		if int(x)%len(resps) == 0 {
			rand.Shuffle(len(resps), func(i, j int) { resps[i], resps[j] = resps[j], resps[i] })
		}
	default:
		x = atomic.AddUint64(idx, 1) - 1
	}
	return resps[int(x)%len(resps)], true
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

func useProxy(proxy *configProxy, w http.ResponseWriter, r *http.Request, headers *headers) error {
	// create the reverse proxy
	xy := httputil.NewSingleHostReverseProxy(proxy._url)

	r.Host = proxy._url.Host
	r.URL.Host = proxy._url.Host
	if headers != nil {
		for k, vals := range headers.Data {
			for _, val := range vals {
				//v, _ := val.Expr.Value(nil)
				r.Header.Set(k, val.AsString())
			}
		}
	}
	if proxy.Headers != nil {
		for k, vals := range proxy.Headers.Data {
			for _, val := range vals {
				//v, _ := val.Expr.Value(nil)
				r.Header.Set(k, val.AsString())
			}
		}
	}
	r.URL.Scheme = proxy._url.Scheme

	log.Printf("[http] [proxy] to %s", proxy._url.String())
	xy.ServeHTTP(w, r)
	return nil
}

func httpHandler(req RequestHTTP) http.HandlerFunc {
	var idx uint64
	var resps = req.Response
	if req.seed == 0 {
		req.seed = time.Now().UnixNano()
	}

	rand.Seed(req.seed) // doesn't have to be crypto-quality random here...

	return WriteError(func(w http.ResponseWriter, r *http.Request) (err error) {
		resp, hasResp := respOrderHTTP(&idx, resps, req.Order)
		if !hasResp {
			return Ext404Error{nil}
		}

		if len(req.Delay) > 0 {
			time.Sleep(delay(req.Delay))
		}

		if resp.Status == "" {
			resp.Status = "200"
		}

		i := resp.Status
		status, err := strconv.ParseInt(i, 10, 16)
		if err != nil {
			if proxy, ok := r.Context().Value(ctxKey(resp.Status)).(*configProxy); ok {
				return useProxy(proxy, w, r, resp.Headers)
			}
			return ErrParseInt.F(err)
		}

		// parse JWT tokens and validate if necessary
		token, err := decodeJWT(w, r, req.JWT)
		if err != nil {
			if _, ok := err.(WarnError); !ok {
				return ErrDecodeJWTResponse.F(err)
			}
		}

		mEvalCtx := make(map[string]cty.Value)

		// check in incoming request headers are matching
		if req.Headers != nil {
			var matchLength = len(req.Headers.Data)
			for key, vals := range req.Headers.Data {
				values := r.Header.Values(key)
				for _, val := range vals {
					//vv, _ := val.Expr.Value(nil)

					for _, v := range values {
						if val.AsString() == "*" || v == val.AsString() {
							matchLength--
							break
						}
					}
				}
			}
			if matchLength != 0 {
				return Ext404Error{ErrHeaderMatchNotFound}
			}
		}

		{ // add url params
			params := chi.RouteContext(r.Context()).URLParams
			if len(params.Keys) > 0 {
				urlCtx := make(map[string]cty.Value)
				for i, k := range params.Keys {
					k = strings.ToLower(k)
					urlCtx[k] = cty.StringVal(params.Values[i])
				}
				mEvalCtx["url"] = cty.ObjectVal(urlCtx)
			}
		}

		{ // add query
			params := r.URL.Query()
			if len(params) > 0 {
				queryCtx := make(map[string]cty.Value)
				for k, vals := range params {
					indexCtx := make(map[string]cty.Value)
					for i, val := range vals {
						indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
					}
					k = strings.ToLower(k)
					queryCtx[k] = cty.ObjectVal(indexCtx)
				}
				mEvalCtx["query"] = cty.ObjectVal(queryCtx)
			}
		}

		{ // add header values
			params := r.Header
			if len(params) > 0 {
				headerCtx := make(map[string]cty.Value)
				for k, vals := range params {
					indexCtx := make(map[string]cty.Value)
					for i, val := range vals {
						indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
					}
					k = strings.ToLower(k)
					headerCtx[k] = cty.ObjectVal(indexCtx)
				}
				mEvalCtx["header"] = cty.ObjectVal(headerCtx)
			}
		}

		{ // add posted values
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil {
					return ErrParseForm.F400(err)
				}
			}
			params := r.Form
			if params != nil && len(params) > 0 {
				postCtx := make(map[string]cty.Value)
				for k, vals := range params {
					indexCtx := make(map[string]cty.Value)
					for i, val := range vals {
						indexCtx[strconv.Itoa(i)] = cty.StringVal(val)
					}
					k = strings.ToLower(k)
					postCtx[k] = cty.ObjectVal(indexCtx)
				}
				mEvalCtx["post"] = cty.ObjectVal(postCtx)
			}
		}

		{ // add jwt values
			if req.JWT != nil && token != nil {
				if params, ok := token.Claims.(jwtgo.MapClaims); ok {
					jwtCtx := make(map[string]cty.Value)
					for k, val := range params {
						if v, ok := val.(string); ok {
							jwtCtx[k] = cty.StringVal(v)
						}
					}
					mEvalCtx["jwt"] = cty.ObjectVal(jwtCtx)
				}
			}
		}

		var mFunCtx = make(map[string]function.Function)
		{ // add from plugins
			type (
				plugFns interface {
					Functions() map[string]function.Function
				}
				plugVars interface {
					Variables() map[string]cty.Value
				}
			)

			for _, plugin := range plugins {
				if plug, ok := plugin.(plugVars); ok {
					for k, v := range plug.Variables() {
						mEvalCtx[k] = v
					}
				}
				if plug, ok := plugin.(plugFns); ok {
					for k, v := range plug.Functions() {
						mFunCtx[k] = v
					}
				}
			}
		}

		hclEvalCtx := respHTTPContext(mEvalCtx, mFunCtx)
		// add headers from the config to the response
		if resp.Headers != nil {
			for key, vals := range resp.Headers.Data {
				for _, val := range vals {
					//v, _ := val.Expr.Value(nil)
					w.Header().Add(key, val.AsString())
				}
			}
		}

		var bodyStr string
		var jwtStr string
		if resp.JWT != nil {
			cfgJWT, _ := r.Context().Value(ctxKey(resp.JWT.Name)).(*configJWT)
			resp.JWT._ctx = hclEvalCtx
			jwtStr, err = marshalJWT(cfgJWT, resp.JWT, r.Context().Value(sigCtxKey))
			if err != nil {
				return ErrBadHCLExpression.F400(err)
			}
			switch resp.JWT.Output {
			case "header":
				w.Header().Add(resp.JWT.Key, jwtStr)
			case "cookie":
				cookie := http.Cookie{
					Name:  resp.JWT.Key,
					Value: jwtStr,
				}
				http.SetCookie(w, &cookie)
			default: // and "body"
				bodyStr = jwtStr
			}
		}

		// pull in the body string
		if resp.Body != nil && bodyStr == "" {
			if _, ok := resp.Body.Expr.(*hclsyntax.TemplateExpr); ok {
				for i, part := range resp.Body.Expr.(*hclsyntax.TemplateExpr).Parts {
					var partVars = part.Variables()
					for _, vars := range partVars {
						var hasIdx bool
						var name string
						for _, v := range vars {
							if root, ok := v.(hcl.TraverseRoot); ok {
								name = root.Name
							}
							if _, ok := v.(hcl.TraverseIndex); ok {
								hasIdx = true
								break
							}
						}

						if !hasIdx && (name == "header" || name == "query" || name == "post") {
							resp.
								Body.
								Expr.(*hclsyntax.TemplateExpr).
								Parts[i].(*hclsyntax.ScopeTraversalExpr).
								Traversal = append(resp.
								Body.
								Expr.(*hclsyntax.TemplateExpr).
								Parts[i].(*hclsyntax.ScopeTraversalExpr).
								Traversal, hcl.TraverseIndex{
								SrcRange: hcl.Range{
									Filename: "test",
								},
								Key: cty.NumberIntVal(0),
							})
						}
					}
				}
			}

			bodyExpr, dia := resp.Body.Expr.Value(hclEvalCtx)
			if dia.HasErrors() {
				return ErrBadHCLExpression.F400(dia)
			}
			switch t := bodyExpr.Type(); t {
			case cty.String:
				bodyStr = bodyExpr.AsString()
			default:
				b, err := json.Marshal(ctyjson.SimpleJSONValue{Value: bodyExpr})
				if err != nil {
					return ErrBadHCLExpression.F400(err)
				}
				bodyStr = string(b)
			}

		}

		w.WriteHeader(int(status))
		fmt.Fprint(w, bodyStr)

		return nil
	})
}
