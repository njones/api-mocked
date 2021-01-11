package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/dgrijalva/jwt-go"
	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

func corsHandler(cors *corsBlock) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", cors.AllowOrigin)
		if cors.AllowMethods != nil {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(cors.AllowMethods, ", "))
		}
		if cors.AllowHeaders != nil {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cors.AllowHeaders, ", "))
		}
		if cors.AllowCredentials != nil {
			w.Header().Set("Access-Control-Allow-Credentials", fmt.Sprint(*cors.AllowCredentials))
		}
		if cors.MaxAge != nil {
			w.Header().Set("Access-Control-Allow-Max-Age", fmt.Sprint(*cors.MaxAge))
		}
	}
}
func reqMiddleware(req request, cors *corsBlock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, vData := range req.Headers.Data {
				data := r.Header.Values(k)
				if len(data) != len(vData) {
					goto Next
				}
				sort.Strings(data)
				sort.Strings(vData)
				for i, v := range data {
					if vData[i] != v {
						goto Next
					}
				}
			}
			reqHandler(req, cors)(w, r)
			return
		Next:
			next.ServeHTTP(w, r)
		})
	}
}

var jwtSigMap = map[string]jwtgo.SigningMethod{
	jwtgo.SigningMethodHS256.Name: jwtgo.SigningMethodHS256,
	jwtgo.SigningMethodHS384.Name: jwtgo.SigningMethodHS384,
	jwtgo.SigningMethodHS512.Name: jwtgo.SigningMethodHS512,

	jwtgo.SigningMethodES256.Name: jwtgo.SigningMethodES256,
	jwtgo.SigningMethodES384.Name: jwtgo.SigningMethodES384,
	jwtgo.SigningMethodES512.Name: jwtgo.SigningMethodES512,

	jwtgo.SigningMethodRS256.Name: jwtgo.SigningMethodRS256,
	jwtgo.SigningMethodRS384.Name: jwtgo.SigningMethodRS384,
	jwtgo.SigningMethodRS512.Name: jwtgo.SigningMethodRS512,

	jwtgo.SigningMethodPS256.Name: jwtgo.SigningMethodPS256,
	jwtgo.SigningMethodPS384.Name: jwtgo.SigningMethodPS384,
	jwtgo.SigningMethodPS512.Name: jwtgo.SigningMethodPS512,
}

func reqHandler(req request, cors *corsBlock) http.HandlerFunc {
	headers, out, order := req.Headers, req.Response, req.Order

	c := respIter(out, order)
	return func(w http.ResponseWriter, r *http.Request) {
		log.Println("REQ URL:", r.URL)
		res, ok := <-c
		if !ok {
			return // we've closed the connection
		}

		rr := res.r.(response)
		status := rr.Status
		i, err := strconv.ParseInt(status, 10, 16)
		if err != nil {
			log.Fatal("parse: status invalid int:", err)
		}

		var m = make(map[string]string)

		if headers != nil {
			for k, vs := range headers.Data {
				for _, v := range vs {
					if strings.HasPrefix(v, "var.") {
						m[strings.Title(strings.ReplaceAll(strings.TrimPrefix(v, "var."), "-", ""))] = r.Header.Get(k)
					}
				}
			}
		}

		if ctx := chi.RouteContext(r.Context()); ctx != nil {
			for i, key := range ctx.URLParams.Keys {
				if key == "*" {
					continue
				}
				m[strings.Title(key)] = ctx.URLParams.Values[i]
			}
		}

		if req.JWT != nil {
			var jwtStr string
			switch req.JWT.Input {
			case "header":
				jwtStr = r.Header.Get(req.JWT.Key)
			case "cookie":
				cookie, err := r.Cookie(req.JWT.Key)
				if err != nil {
					log.Println("cookie:", err)
				}
				jwtStr = cookie.Value
			case "auth":
				data := r.Header.Get("Authorization")
				switch req.JWT.Key {
				case "bearer":
					ab := strings.SplitN(data, " ", 2)
					if strings.ToLower(ab[0]) == "bearer" {
						jwtStr = ab[1]
					}
				}
			}

			if jwtStr != "" {
				claims := jwt.MapClaims{}
				token, err := jwt.ParseWithClaims(jwtStr, claims, func(token *jwt.Token) (interface{}, error) {
					key := r.Context().Value(sigCtxKey)
					switch k := key.(type) {
					case []byte:
						return k, nil
					case *rsa.PrivateKey:
						return k, nil
					}
					return nil, fmt.Errorf("invalid key")
				})
				if err != nil {
					log.Println("err:", err)
				}

				if req.JWT.Validate {
					if !token.Valid {
						r.Header.Set("x-invalid-jwt", "true")
						goto SkipSettingClaims
					}
				}

				for k, v := range claims {
					m[strings.Title(req.JWT.Prefix)+strings.Title(k)] = fmt.Sprintf("%v", v)
				}

			SkipSettingClaims:
			}

		}

		var body string
		if rr.Body != nil {
			ctyText, err := rr.Body.Expr.Value(&bodyContext)
			if err != nil {
				log.Fatal("cty body:", err)
			}
			body = ctyText.AsString()

			if len(m) > 0 {
				t, err := template.New("body").Parse(body)
				if err != nil {
					log.Fatalf("template: %v", err)
				}
				buf := new(bytes.Buffer)
				t.Execute(buf, m)
				body = buf.String()
			}
		}

		if rr.JWT != nil {
			m := make(map[string]interface{})

			for k, array := range map[string][]string{
				"roles":     rr.JWT.Roles,
				"auth_type": rr.JWT.AuthType,
			} {
				if array != nil {
					var ss []string
					for _, v := range array {
						ss = append(ss, v)
					}
					m[k] = ss
				}
			}

			for k, v := range map[string]*hcl.Attribute{
				"iss": rr.JWT.Issuers,
				"sub": rr.JWT.Subject,
				"aud": rr.JWT.Audience,
				"exp": rr.JWT.Expiration,
				"nbf": rr.JWT.NotBefore,
				"iat": rr.JWT.IssuedAt,
				"jwi": rr.JWT.JWTID,
			} {
				if v != nil {
					val, err := v.Expr.Value(&jwtContext) // empty context
					if err != nil {
						panic(err)
					}
					if val.Type() == cty.Number {
						m[k], _ = val.AsBigFloat().Int64()
					} else {
						m[k] = val.AsString()
					}
				}
			}
			for k, v := range rr.JWT.Payload {
				m[k] = v
			}

			if jwtc, ok := r.Context().Value(ctxKey(rr.JWT.Name)).(*jwtConfig); ok {
				key := r.Context().Value(sigCtxKey)
				switch k := key.(type) {
				case []byte:
					m["$._internal."+jwtc.Name+".key"] = string(key.([]byte))
				case *rsa.PrivateKey:
					b := pem.EncodeToMemory(
						&pem.Block{
							Type:  "RSA PRIVATE KEY",
							Bytes: x509.MarshalPKCS1PrivateKey(k),
						},
					)
					m["$._internal."+jwtc.Name+".key"] = string(b)
				case *ecdsa.PrivateKey:
					m["$._internal."+jwtc.Name+".key"] = "ECDSA PEM"
				}

				if algo, ok := jwtSigMap[jwtc.Alg]; ok {
					token := jwtgo.NewWithClaims(algo, jwtgo.MapClaims(m))
					tokStr, err := token.SignedString(key)
					if err != nil {
						log.Printf("jwt error: %v", err)
					}

					switch rr.JWT.Output {
					case "header":
						w.Header().Add(rr.JWT.Key, tokStr)
					case "cookie":
						cookie := http.Cookie{
							Name:  rr.JWT.Key,
							Value: tokStr,
						}
						http.SetCookie(w, &cookie)
					default:
						fmt.Fprint(w, tokStr)
					}
				}
			}
		}

		if rr.Headers != nil {
			for k, vs := range rr.Headers.Data {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
		}

		if len(req.Delay) > 0 {
			time.Sleep(delay(req.Delay))
		}

		if cors != nil {
			if cors.AllowOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", cors.AllowOrigin)
			}
			if cors.AllowMethods != nil {
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(cors.AllowMethods, ", "))
			}
			if cors.AllowHeaders != nil {
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(cors.AllowHeaders, ", "))
			}
			if cors.AllowCredentials != nil {
				w.Header().Set("Access-Control-Allow-Credentials", fmt.Sprint(*cors.AllowCredentials))
			}
			if cors.MaxAge != nil {
				w.Header().Set("Access-Control-Allow-Max-Age", fmt.Sprint(*cors.MaxAge))
			}
		}

		w.WriteHeader(int(i))
		fmt.Fprint(w, body)
	}
}

var interval = map[string]time.Duration{
	"ns": time.Nanosecond,
	"ms": time.Millisecond,
	"µ":  time.Microsecond,
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

func wsHandler(req request) http.HandlerFunc {
	out, order := req.SocketIO, req.Order

	wsResp := respIter(out, order)
	return func(w http.ResponseWriter, r *http.Request) {
		log.Println("WS URL:", r.URL)

		go func() {
		Tock:
			res, ok := <-wsResp
			if !ok {
				return // we've closed the connection
			}
			rr := res.r.(socketio)

			if len(req.Delay) > 0 {
				time.Sleep(delay(req.Delay))
			}

			if rr.Broadcast != nil {
				for _, broadcast := range rr.Broadcast {
					conn.BroadcastTo(broadcast.Room, broadcast.Event, convertToJSON(broadcast.Args))
				}
			}

			if rr.BroadcastAll != nil {
				for _, broadcast := range rr.BroadcastAll {
					conn.BroadcastToAll(broadcast.Event, convertToJSON(broadcast.Args))
				}
			}

			if req.Ticker != nil && len(req.Ticker.Time) > 0 {
				time.Sleep(delay(req.Ticker.Time))
				goto Tock
			}
		}()
	}
}

func pnHandler(req request, clientPN *pnClient) http.HandlerFunc {
	out, order := req.PubNub, req.Order

	pnResp := respIter(out, order)
	return func(w http.ResponseWriter, r *http.Request) {
		log.Println("PN URL:", r.URL)

		timer := time.NewTimer(1 * time.Minute)
		timeout := timer.C
		go func() {
			defer timer.Stop()

		Tock:
			res, ok := <-pnResp
			if !ok {
				return // we've closed the connection
			}
			rr := res.r.(pubnub)

			conn := clientPN.Get(rr.Name)
			if rr.PublishSocketIO != nil {
				for _, bcast := range rr.PublishSocketIO {

					var data string
					if bcast.Data != nil {
						ctyText, err := bcast.Data.Expr.Value(&bodyContext)
						if err != nil {
							log.Fatal("cty pubnub data:", err)
						}
						data = ctyText.AsString()
					}

					var dataMap map[string]interface{}
					json.Unmarshal([]byte(data), &dataMap)

					var dataArray []interface{}
					json.Unmarshal([]byte(data), &dataArray)

					msg := map[string]interface{}{
						"name": bcast.Event,
						"ns":   bcast.Namespace,
						"data": data,
					}

					if len(dataMap) > 0 {
						msg["data"] = dataMap
					}

					if len(dataArray) > 0 {
						msg["data"] = dataArray
					}

					_, _, err := conn.Publish().Channel(clientPN.channel[rr.Name]).Message(msg).Execute()
					if err != nil {
						panic(err)
					}
				}
			}

			if len(timeout) == 0 && req.Ticker != nil && len(req.Ticker.Time) > 0 {
				time.Sleep(delay(req.Ticker.Time))
				goto Tock
			}
		}()
	}
}

type rtn struct {
	n int
	m int
	r interface{}
}

// TODO(njones): make this code generated so it's
// clear as to what is happening and is 'generic'
// respIter does the iteration via a channel.
func respIter(o interface{}, order string) chan rtn {
	n := 0
	c := make(chan rtn, 1)
	var x int
	switch oo := o.(type) {
	case []response:
		x = len(oo)
	case []socketio:
		x = len(oo)
	case []pubnub:
		x = len(oo)
	default:
		panic(fmt.Sprintf("bad response to iterate over: %T", o))
	}

	go func() {
		switch order {
		case "random":
			rand.Seed(time.Now().UnixNano())
			switch oo := o.(type) {
			case []response:
				for {
					c <- rtn{n, x, oo[rand.Intn(len(oo))]}
					n++
				}
			case []socketio:
				for {
					c <- rtn{n, x, oo[rand.Intn(len(oo))]}
					n++
				}
			case []pubnub:
				for {
					c <- rtn{n, x, oo[rand.Intn(len(oo))]}
					n++
				}
			}
		case "unordered":
			a := make([]int, x)
			for {
				rand.Seed(time.Now().UnixNano())
				for j := range a {
					a[j] = j
				}
				rand.Shuffle(len(a), func(i, j int) { a[i], a[j] = a[j], a[i] })

				switch oo := o.(type) {
				case []response:
					for _, i := range a {
						c <- rtn{n, x, oo[i]}
						n++
					}
				case []socketio:
					for _, i := range a {
						c <- rtn{n, x, oo[i]}
						n++
					}
				case []pubnub:
					for _, i := range a {
						c <- rtn{n, x, oo[i]}
						n++
					}
				}
			}
		default:
			switch oo := o.(type) {
			case []response:
				for {
					for _, v := range oo {
						c <- rtn{n, x, v}
						n++
					}
				}
			case []socketio:
				for {
					for _, v := range oo {
						c <- rtn{n, x, v}
						n++
					}
				}
			case []pubnub:
				for {
					for _, v := range oo {
						c <- rtn{n, x, v}
						n++
					}
				}
			}
		}
	}()
	return c
}
