package main

import (
	"fmt"
	"html/template"
	"math/rand"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/zclconf/go-cty/cty"
)

func httpHandler(req request) http.HandlerFunc {
	var idx = int64(-1)
	var resps = req.Response
	if req.Order == "unordered" {
		rand.Seed(time.Now().UnixNano()) // doesn't have to be crypto-quality random here...
	}
	return WriteError(func(w http.ResponseWriter, r *http.Request) error {
		var x int64
		switch req.Order {
		case "random":
			x = rand.Int63n(int64(len(resps) * 2))
		case "unordered":
			x = atomic.AddInt64(&idx, 1)
			if int(x)%len(resps) == 0 {
				rand.Shuffle(len(resps), func(i, j int) { resps[i], resps[j] = resps[j], resps[i] })
			}
		default:
			x = atomic.AddInt64(&idx, 1)
		}
		resp := resps[int(x)%len(resps)]

		if len(req.Delay) > 0 {
			time.Sleep(delay(req.Delay))
		}

		// parse JWT tokens and validate if necessary
		token, err := decodeJWT(r, req.JWT)
		if err != nil {
			return ErrDecodeJWTResponse.F(err)
		}

		// Add all of the template functions
		var bodyFuncs template.FuncMap = map[string]interface{}{
			"Header":     func(s string) string { return r.Header.Get(s) },
			"UrlParam":   func(s string) string { return chi.URLParam(r, s) },
			"QueryParam": func(s string) string { return r.URL.Query().Get(s) },
			"JwtField": func(s string) string {
				if token != nil {
					if claims, ok := token.Claims.(jwtgo.MapClaims); ok {
						if value, ok := claims[s]; ok {
							switch val := value.(type) {
							case string:
								return val
							case int:
								return strconv.Itoa(val)
							}
						}
					}
				}
				return ""
			},
		}

		// sending back a response ...

		// pull in the body string
		var body string
		if resp.Body != nil {
			bodyExpr, err := resp.Body.Expr.Value(&bodyEvalCtx)
			if err != nil {
				return ErrBadHCLExpression.F400(err)
			}
			body = bodyExpr.AsString()
		}

		// start to run the string through the template
		t, err := template.New("body").Funcs(bodyFuncs).Parse(body)
		if err != nil {
			return ErrTemplateParse.F400(err)
		}

		// check for header variables
		var skipHeaderValidation = make(map[string]struct{})
		for _, node := range t.Tree.Root.Nodes {
			_node := node.String()
			if strings.HasPrefix(_node, "{{Header") && strings.HasSuffix(_node, "}}") {
				skipHeaderValidation[textproto.CanonicalMIMEHeaderKey(_node[10:len(_node)-3])] = struct{}{}
			}
		}

		// check in incoming request headers are matching
		if req.Headers != nil {
			var matchLength = len(req.Headers.Data)
			for key, vals := range req.Headers.Data {
				values := r.Header.Values(key)
				for _, val := range vals {
					for _, v := range values {
						if _, ok := skipHeaderValidation[key]; v == val || ok {
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

		// add headers from the config to the response
		if resp.Headers != nil {
			for key, values := range resp.Headers.Data {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
		}

		// output a JWT token if we need to..
		if resp.JWT != nil {
			cfg, _ := r.Context().Value(ctxKey(resp.JWT.Name)).(*jwtConfig)
			key := r.Context().Value(sigCtxKey)
			resp.JWT._hclVarMap = make(map[string]map[string]cty.Value)
			ups := chi.RouteContext(r.Context()).URLParams
			if len(ups.Keys) > 0 {
				resp.JWT._hclVarMap["urlparam"] = make(map[string]cty.Value)
				for i, k := range ups.Keys {
					resp.JWT._hclVarMap["urlparam"][k] = cty.StringVal(ups.Values[i])
				}
			}
			qys := r.URL.Query()
			if len(qys) > 0 {
				resp.JWT._hclVarMap["queryparam"] = make(map[string]cty.Value)
				for k, vals := range qys {
					var idxMap = make(map[string]cty.Value)
					for i, val := range vals {
						var key = strconv.Itoa(i)
						if i == 0 {
							key = "val"
						}
						idxMap[key] = cty.StringVal(val)
					}
					resp.JWT._hclVarMap["queryparam"][k] = cty.ObjectVal(idxMap)
				}
			}

			// TODO(njones): add headers... not here, because we don't want to
			// pre-parse all of them... what's the best way forward

			if err := encodeJWT(w, resp.JWT, cfg, key); err != nil {
				return ErrEncodeJWTResponse.F(err)
			}
		}

		status := resp.Status
		i, err := strconv.ParseInt(status, 10, 16)
		if err != nil {
			return ErrParseInt.F(err)
		}
		w.WriteHeader(int(i))

		if err = t.ExecuteTemplate(w, "body", nil); err != nil {
			return ErrTemplateParse.F400(fmt.Errorf(">> %v", err))
		}

		return nil
	})
}
