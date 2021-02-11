package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

type testHTTP struct {
	name string

	config struct {
		jwt struct {
			algo   string
			secret interface{}
		}
		path string
		req  request
	}

	http struct {
		jwt struct {
			key   string
			token string
		}
		headers http.Header
		req     struct {
			method string
			url    string
			body   io.Reader
		}
	}

	want struct {
		validation string
		statusCode int
		body       string
	}
}

type testOpt func(*testHTTP)

func test(t *testing.T, name string, opts ...testOpt) testHTTP {
	resp := testHTTP{name: name}

	resp.config.req.Response = []response{
		{
			Status: "200",
			Body:   attr("Hello, World"),
		},
	}
	resp.config.path = "/this/is/standard/World"
	resp.http.req.url = "/this/is/standard/World"

	resp.config.req.Method = "get"
	resp.http.req.method = "get"

	resp.want.statusCode = 200
	resp.want.body = "Hello, World"

	resp.config.req.JWT = &jwtRequest{
		Input: "auth",
		Key:   "bearer",
	}

	resp.config.jwt.algo = "HS256"
	resp.config.jwt.secret = []byte("the secret string")

	type testClaims struct {
		Hello string `json:"hello"`
		jwt.StandardClaims
	}

	expirationTime := time.Now().Add(5 * time.Minute)
	// Create the JWT claims, which includes the username and expiry time
	claims := &testClaims{
		Hello: "World",
		StandardClaims: jwt.StandardClaims{
			// In JWT, the expiry time is expressed as unix milliseconds
			ExpiresAt: expirationTime.Unix(),
		},
	}

	// Declare the token with the algorithm used for signing, and the claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Create the JWT string
	tokenStr, err := token.SignedString(resp.config.jwt.secret)
	if err != nil {
		t.Fatal(err)
	}

	resp.http.jwt.key = resp.config.req.JWT.Input
	resp.http.jwt.token = tokenStr

	for _, opt := range opts {
		opt(&resp)
	}

	return resp
}

// reqHeader enter k, v ... k, v and it will return the map
func reqHeader(kvs ...string) map[string][]string {
	rtn := make(map[string][]string)
	for i, v := range kvs {
		if i%2 == 1 {
			k := kvs[i-1]
			if _, ok := rtn[k]; ok {
				rtn[k] = append(rtn[k], v)
				continue
			}
			rtn[k] = []string{v}
		}
	}
	return rtn
}

var attr = func(s string) *hcl.Attribute {
	return &hcl.Attribute{Name: "body", Expr: hcl.StaticExpr(cty.StringVal(s), hcl.Range{Start: hcl.InitialPos, End: hcl.Pos{Byte: 0, Line: 1, Column: len(s)}})}
}

func testPath(path string) testOpt {
	return func(tr *testHTTP) {
		tr.config.path = path
	}
}

func testURL(url string) testOpt {
	return func(tr *testHTTP) {
		tr.http.req.url = url
	}
}

func testHeaders(httpHeader http.Header, reqHeader map[string][]string) testOpt {
	return func(tr *testHTTP) {
		tr.config.req.Headers = &headers{Data: reqHeader}
		tr.http.headers = httpHeader
	}
}

func testResponse(responses ...response) testOpt {
	return func(tr *testHTTP) {
		if len(responses) == 0 {
			responses = nil
		}
		tr.config.req.Response = responses
	}
}

func testWant(status int, body string) testOpt {
	return func(tr *testHTTP) {
		tr.want.statusCode = status
		tr.want.body = body
	}
}

func testJWTInput(input, key string) testOpt {
	return func(tr *testHTTP) {
		tr.config.req.JWT.Key = key
		tr.config.req.JWT.Input = input
	}
}

func testJWTSecret(secret interface{}) testOpt {
	return func(tr *testHTTP) {
		tr.config.jwt.secret = secret
	}
}

func TestRequestHandler(t *testing.T) {

	var tests = []testHTTP{
		test(t, "normal"),
		test(t, "header",
			testHeaders(
				http.Header{"A": {"b"}},
				reqHeader("a", "b")),
		),
		test(t, "no header",
			testHeaders(
				http.Header{"A": {"b"}},
				reqHeader("c", "d")),
			testWant(404, "404 page not found\n"),
		),
		test(t, "no body",
			testHeaders(
				http.Header{"A": {"b"}},
				reqHeader("c", "d")),
			testResponse(),
			testWant(404, "404 page not found\n"),
		),
		test(t, "header template",
			testHeaders(
				http.Header{"A": {"World"}},
				reqHeader("A", "d")),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ Header "a" }}`),
			}),
		),

		// URL/Query params
		test(t, "bad path",
			testURL("/this/is/different"),
			testWant(404, "404 page not found\n"),
		),
		test(t, "queryparam template",
			testURL("/this/is/standard/World?hello=World"),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ Query "hello" }}`),
			}),
		),
		test(t, "queryparam invalid template",
			testURL("/this/is/standard/World?playground=World"),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ Query "hello" }}`),
			}),
			testWant(200, "Hello, "),
		),
		test(t, "urlparam template",
			testPath("/this/is/standard/{id}"),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ URL "id" }}`),
			}),
		),

		// JWT Token
		test(t, "jwt auth template",
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ JWT "hello" }}`),
			}),
		),
		test(t, "jwt cookie template",
			testJWTInput("cookie", "works"),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ JWT "hello" }}`),
			}),
		),
		test(t, "jwt header template",
			testJWTInput("header", "x-jwt-test-header"),
			testResponse(response{
				Status: "200", Body: attr(`Hello, {{ JWT "hello" }}`),
			}),
		),
		test(t, "jwt invalid secret",
			testJWTSecret([]byte("different")),
			testWant(500, "Internal server error\n"),
		),
		test(t, "jwt validation is valid",
			func(tr *testHTTP) {
				var T = true
				tr.config.req.JWT.Validate = &T
				tr.want.validation = "valid"
			},
		),
		test(t, "jwt validation is invalid",
			testJWTSecret([]byte("different")),
			func(tr *testHTTP) {
				var T = true
				tr.config.req.JWT.Validate = &T
				tr.want.validation = "invalid"
			},
		),
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(strings.ToUpper(test.http.req.method), test.http.req.url, test.http.req.body)
			if err != nil {
				t.Fatal(err)
			}

			if test.http.headers != nil {
				req.Header = test.http.headers
			}

			if test.config.req.JWT != nil {
				switch test.config.req.JWT.Input {
				case "auth":
					req.Header.Set("Authorization", test.config.req.JWT.Key+" "+test.http.jwt.token)
				case "cookie":
					cookie := http.Cookie{
						Name:  test.config.req.JWT.Key,
						Value: test.http.jwt.token,
					}
					req.AddCookie(&cookie)
				case "header":
					req.Header.Set(test.config.req.JWT.Key, test.http.jwt.token)
				}
				ctx := context.WithValue(req.Context(), sigCtxKey, test.config.jwt.secret)
				req = req.WithContext(ctx)
			}

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.config.req.Method, test.config.path, httpHandler(test.config.req))
			hdl.ServeHTTP(rec, req)

			if rec.Code != test.want.statusCode {
				t.Errorf("have: %d want: %d", rec.Code, test.want.statusCode)
			}

			if rec.Body.String() != test.want.body {
				t.Errorf("\nhave: %q\nwant: %q", rec.Body.String(), test.want.body)
			}

			if test.config.req.JWT != nil {
				if test.config.req.JWT.Validate != nil && *test.config.req.JWT.Validate {
					have := rec.Header().Get("x-jwt-validation")
					if have != test.want.validation {
						t.Errorf("\nhave: %q\nwant: %q", have, test.want.validation)
					}
				}
			}

		})
	}
}
