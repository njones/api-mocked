package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

type testHTTP struct {
	name string

	config struct {
		jwt struct {
			algo   string
			secret interface{}
		}
		path string
		req  RequestHTTP
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
		header     http.Header
	}
}

type testOpt func(*testHTTP)

func test(t *testing.T, name string, opts ...testOpt) testHTTP {
	resp := testHTTP{name: name}

	resp.config.req.Response = []ResponseHTTP{
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

	resp.config.req.JWT = &requestJWT{
		Input: "auth",
		Key:   "bearer",
	}

	resp.config.jwt.algo = "HS256"
	resp.config.jwt.secret = []byte("the secret string")

	type testClaims struct {
		Hello string `json:"hello"`
		jwtgo.StandardClaims
	}

	expirationTime := time.Now().Add(5 * time.Minute)
	// Create the JWT claims, which includes the username and expiry time
	claims := &testClaims{
		Hello: "World",
		StandardClaims: jwtgo.StandardClaims{
			// In JWT, the expiry time is expressed as unix milliseconds
			ExpiresAt: expirationTime.Unix(),
		},
	}

	// Declare the token with the algorithm used for signing, and the claims
	token := jwtgo.NewWithClaims(jwtgo.SigningMethodHS256, claims)
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
				// rtn[k] = append(rtn[k], attr(v))
				rtn[k] = append(rtn[k], v)
				continue
			}
			// rtn[k] = []*hcl.Attribute{attr(v)}
			rtn[k] = []string{v}
		}
	}
	return rtn
}

var attr = func(s string) *hcl.Attribute {
	a, _ := hclsyntax.ParseTemplate([]byte(s), "test", hcl.Pos{})
	return &hcl.Attribute{Name: "body", Expr: a}
}

var attrE = func(s string) *hcl.Attribute {
	a, _ := hclsyntax.ParseExpression([]byte(s), "test", hcl.Pos{})
	return &hcl.Attribute{Name: "body", Expr: a}
}

func testPath(path string) testOpt {
	return func(tr *testHTTP) {
		tr.config.path = path
	}
}

func testPost(m map[string][]string) testOpt {
	var q = new(url.URL).Query()
	for k, vs := range m {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return func(tr *testHTTP) {
		tr.http.req.body = strings.NewReader(q.Encode())
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

func testMethod(method string, opts ...testOpt) testOpt {
	return func(tr *testHTTP) {
		tr.http.req.method = method
		tr.config.req.Method = method
		if tr.http.headers == nil {
			tr.http.headers = http.Header{}
		}
		tr.http.headers.Set("content-type", "application/x-www-form-urlencoded")
		for _, opt := range opts {
			opt(tr)
		}
	}
}

func testResponse(responses ...ResponseHTTP) testOpt {
	return func(tr *testHTTP) {
		if len(responses) == 0 {
			responses = nil
		}
		tr.config.req.Response = responses
	}
}

func testResponseHeaders(m ...map[string][]string) testOpt {
	return func(tr *testHTTP) {
		for i, v := range m {
			tr.config.req.Response[i].Headers = &headers{Data: v}
		}
	}
}

func testWant(status int, body string) testOpt {
	return func(tr *testHTTP) {
		tr.want.statusCode = status
		tr.want.body = body
	}
}

func testWantHeaders(m map[string][]string) testOpt {
	return func(tr *testHTTP) {
		tr.want.header = http.Header(m)
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

type testPluginData struct{}

func (testPluginData) Setup() error                       { return nil }
func (testPluginData) SetupRoot(hcl.Body) error           { return nil }
func (testPluginData) SetupConfig(string, hcl.Body) error { return nil }

func (testPluginData) Variables() map[string]cty.Value {
	return map[string]cty.Value{
		"test_plugin": cty.ObjectVal(map[string]cty.Value{
			"nested": cty.ObjectVal(map[string]cty.Value{
				"value": cty.StringVal("World"),
			}),
		}),
	}
}

func (testPluginData) Functions() map[string]function.Function {
	return map[string]function.Function{
		"test_plugin_to_hex_func": function.New(&function.Spec{
			Params: []function.Parameter{
				{
					Name: "string",
					Type: cty.String,
				},
			},
			Type: function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {

				return cty.StringVal(hex.EncodeToString([]byte(args[0].AsString()))), nil
			},
		}),
	}
}

func testPlugin() testOpt {
	return func(tr *testHTTP) {
		plugins = map[string]Plugin{
			"testPlugin": testPluginData{},
		}
	}
}

func TestRequestHandler(t *testing.T) {

	var tests = []testHTTP{
		test(t, "normal"),

		test(t, "body HCL object",
			testResponse(ResponseHTTP{
				Status: "200",
				Body: attrE(`{
					 hello = "world"
					 gold = "silver"
					 silver = "gold"
				 }`),
			}),
			testWant(200, `{"gold":"silver","hello":"world","silver":"gold"}`),
		),

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
				reqHeader("A", "*")),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${header.a}`),
			}),
		),
		test(t, "header template with index",
			testHeaders(
				http.Header{"A": {"World"}},
				reqHeader("A", "*")),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${header.a.0}`),
			}),
		),

		test(t, "headers in response",
			testResponseHeaders(reqHeader("x-response-1", "hello, world")),
			testWantHeaders(reqHeader("x-response-1", "hello, world")),
		),

		// URL/Query params
		test(t, "bad path",
			testURL("/this/is/different"),
			testWant(404, "404 page not found\n"),
		),
		test(t, "queryparam template",
			testURL("/this/is/standard/World?hello=World"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${query.hello}"),
			}),
		),
		test(t, "queryparam template with index",
			testURL("/this/is/standard/World?hello=World"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${query.hello.0}"),
			}),
		),
		test(t, "queryparam invalid template",
			testURL("/this/is/standard/World?playground=World"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${query.hello}"),
			}),
			testWant(400, "Bad Request\n"),
		),
		test(t, "urlparam template",
			testPath("/this/is/standard/{id}"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${url.id}`),
			}),
		),

		// Post params
		test(t, "post template",
			testMethod("POST", testPost(reqHeader("hello", "World"))),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${post.hello}"),
			}),
		),
		test(t, "post template with index",
			testMethod("POST", testPost(reqHeader("hello", "World"))),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${post.hello.0}"),
			}),
		),
		test(t, "post invalid template",
			testMethod("POST", testPost(reqHeader("playground", "World"))),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${post.hello}"),
			}),
			testWant(400, "Bad Request\n"),
		),

		// JWT Token
		test(t, "jwt auth template",
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${jwt.hello}`),
			}),
		),
		test(t, "jwt cookie template",
			testJWTInput("cookie", "works"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${jwt.hello}`),
			}),
		),
		test(t, "jwt head template",
			testJWTInput("header", "x-jwt-test-header"),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${jwt.hello}`),
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

			if test.want.header != nil {
				have := rec.Header()
				if len(test.want.header) != len(have) {
					t.Errorf("\nhave: %#v\nwant: %#v", have, test.want.header)
				}

				for k, want := range test.want.header {
					if !reflect.DeepEqual(want, have.Values(k)) {
						t.Errorf("\nhave: %#v\nwant: %#v", have.Values(k), want)
					}
				}
			}

		})
	}
}

func TestRequestHandlerPlugins(t *testing.T) {
	_plugins := plugins
	defer func() { plugins = _plugins }()

	var tests = []testHTTP{
		test(t, "plugin variable",
			testPlugin(),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr("Hello, ${test_plugin.nested.value}"),
			}),
		),
		test(t, "plugin function",
			testPlugin(),
			testResponse(ResponseHTTP{
				Status: "200", Body: attr(`Hello, ${test_plugin_to_hex_func("World")}`),
			}),
			testWant(200, "Hello, 576f726c64"),
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

			if test.want.header != nil {
				have := rec.Header()
				if len(test.want.header) != len(have) {
					t.Errorf("\nhave: %#v\nwant: %#v", have, test.want.header)
				}

				for k, want := range test.want.header {
					if !reflect.DeepEqual(want, have.Values(k)) {
						t.Errorf("\nhave: %#v\nwant: %#v", have.Values(k), want)
					}
				}
			}

		})
	}
}

// The tests for JWT use a static file, this allows for you to type
// 'go generate' to create it so the tests will work as expected

//go:generate go test -run TestMakeJWT -args generate
func TestMakeJWT(t *testing.T) {
	if os.Args[len(os.Args)-1] != "generate" {
		return
	}

	if err := os.MkdirAll("./testdata", 0777); err != nil {
		t.Fatal(err)
	}

	testKey := []byte("This is for testing only")

	// Create the Claims
	claims := &jwtgo.StandardClaims{
		ExpiresAt: time.Now().Add(43800 * time.Hour).Unix(),
		Issuer:    "test",
		Subject:   "let me in",
	}

	token := jwtgo.NewWithClaims(jwtgo.SigningMethodHS256, claims)
	testJWT, err := token.SignedString(testKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := ioutil.WriteFile("./testdata/static.jwt.txt", []byte(testJWT), 0777); err != nil {
		t.Fatal(err)
	}
}

func getJWTTestToken(t *testing.T) string {
	b, err := ioutil.ReadFile("./testdata/static.jwt.txt")
	if err != nil {
		t.Fatal()
	}
	return string(b)
}

func TestResponseOrder(t *testing.T) {

	type wanted struct {
		status []int
		body   []string
		body2  []string
	}

	var tests = []struct {
		name   string
		method string
		req    RequestHTTP
		want   wanted
	}{
		{
			name:   "ordered",
			method: "get",
			req: RequestHTTP{
				Method: "get",
				Order:  "ordered",
				Response: []ResponseHTTP{
					{Status: "200", Body: attr("1")},
					{Status: "200", Body: attr("2")},
					{Status: "200", Body: attr("3")},
				},
			},
			want: wanted{
				status: []int{200, 200, 200, 200, 200, 200},
				body:   []string{"1", "2", "3", "1", "2", "3"},
			},
		},
		{
			name:   "unordered",
			method: "get",
			req: RequestHTTP{
				Method: "get",
				Order:  "unordered",
				Response: []ResponseHTTP{
					{Status: "200", Body: attr("1")},
					{Status: "200", Body: attr("2")},
					{Status: "200", Body: attr("3")},
				},
			},
			want: wanted{
				status: []int{200, 200, 200, 200, 200, 200},
				body:   []string{"1", "1", "2", "2", "3", "3"},
				body2:  []string{"1", "2", "3", "1", "2", "3"}, // should not equal this
			},
		},
		{
			name:   "random order",
			method: "get",
			req: RequestHTTP{
				Method: "get",
				Order:  "random",
				Response: []ResponseHTTP{
					{Status: "200", Body: attr("1")},
					{Status: "200", Body: attr("2")},
					{Status: "200", Body: attr("3")},
				},
				seed: 100,
			},
			want: wanted{
				status: []int{200, 200, 200, 200, 200, 200},
				body:   []string{"1", "3", "1", "2", "1", "3"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			req, err := http.NewRequest(strings.ToUpper(test.method), "/test", nil)
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.req.Method, "/test", httpHandler(test.req))

			var haveStatus []int
			var haveBody []string

			for i := 0; i < len(test.req.Response)*2; i++ {
				rec.Body.Reset()
				hdl.ServeHTTP(rec, req)
				haveStatus = append(haveStatus, rec.Result().StatusCode)
				haveBody = append(haveBody, rec.Body.String())
			}

			if len(haveBody) != len(test.want.body) {
				t.Errorf("have: %v want: %v", haveBody, test.want.body)
			}

			if test.req.Order != "unordered" {

				// This checks for ordered and random (random has a known seed, so it's like ordered)

				for i, want := range test.want.status {
					if len(haveStatus) <= i { // incase we have uneven lengths...
						break
					}
					have := haveStatus[i]
					if have != want {
						t.Errorf("have: %v want: %v", have, want)
					}
				}

				for i, want := range test.want.body {
					if len(haveBody) <= i { // incase we have uneven lengths...
						break
					}
					have := haveBody[i]
					if have != want {
						t.Errorf("have: %v want: %v", have, want)
					}
				}
			}

			if test.req.Order == "unordered" {

				var iHave int
				for i, want := range test.want.body2 {
					if len(haveStatus) <= i { // incase we have uneven lengths...
						break
					}
					have := haveBody[i]
					if have == want {
						iHave++ // everytime we match we increment, we we match everything, then we'll come out with the same number as we have items
					}
				}

				if iHave == len(test.want.body2) {
					t.Errorf(`have: "ordered" want: "unordered"`)
				}

				sort.Ints(haveStatus)
				sort.Strings(haveBody)

				for i, want := range test.want.status {
					if len(haveStatus) <= i { // incase we have uneven lengths...
						break
					}
					have := haveStatus[i]
					if have != want {
						t.Errorf("have: %v want: %v", have, want)
					}
				}
				for i, want := range test.want.body {
					if len(haveBody) <= i { // incase we have uneven lengths...
						break
					}
					have := haveBody[i]
					if have != want {
						t.Errorf("have: %v want: %v", have, want)
					}
				}
			}
		})
	}
}

func TestJWTAuth(t *testing.T) {
	tokenStr := getJWTTestToken(t)

	type want struct {
		status int
		body   string
	}

	var tests = []struct {
		name   string
		method string
		auth   string
		jwt    string
		body   io.Reader
		req    RequestHTTP
		want   want
	}{
		{
			name:   "auth",
			method: "post",
			body:   strings.NewReader("This is a test"),
			auth:   "auth",
			jwt:    tokenStr,
			req: RequestHTTP{
				Method: "post",
			},
			want: want{
				status: 200,
				body:   "This is a test",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			req, err := http.NewRequest(strings.ToUpper(test.method), "/test", test.body)
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.req.Method, "/test", httpHandler(test.req))
			hdl.ServeHTTP(rec, req)

		})
	}
}

func TestJWTResponse(t *testing.T) {

	var stdResJWT = responseJWT{
		Name:    "test-1",
		Subject: attr("sub 1"),
		Payload: map[string]string{
			"$._internal.test-1.key": "Password/Secret",
		},
	}
	var stdCfgJWT = configJWT{
		Name:   "test-1",
		Secret: attr("Password/Secret"),
		Alg:    jwtgo.SigningMethodHS256.Name,
	}

	token := jwtgo.NewWithClaims(jwtgo.SigningMethodHS256, &stdResJWT)
	tokenStr, err := token.SignedString([]byte("Password/Secret"))
	if err != nil {
		t.Fatal(err)
	}

	type wanted struct {
		body    *string
		header  map[string][]string
		cookies map[string]*http.Cookie
	}

	var tests = []struct {
		name   string
		method string
		jwtC   configJWT
		jwtR   responseJWT
		req    RequestHTTP
		reqOut string
		reqKey string
		want   wanted
	}{
		{
			name:   "auth",
			method: "get",
			jwtC:   stdCfgJWT,
			jwtR:   stdResJWT,
			reqOut: "body",
			req:    RequestHTTP{},
			want: wanted{
				body: func() *string {
					return &tokenStr
				}(),
			},
		},
		{
			name:   "auth",
			method: "get",
			jwtC:   stdCfgJWT,
			jwtR:   stdResJWT,
			reqOut: "header",
			reqKey: "x-jwt-test-header",
			req:    RequestHTTP{},
			want: wanted{
				header: func() map[string][]string {
					return map[string][]string{
						"x-jwt-test-header": {tokenStr},
					}
				}(),
			},
		},
		{
			name:   "auth",
			method: "get",
			jwtC:   stdCfgJWT,
			jwtR:   stdResJWT,
			reqOut: "cookie",
			reqKey: "cookie_jwt_test",
			req:    RequestHTTP{},
			want: wanted{
				cookies: func() map[string]*http.Cookie {
					c := http.Cookie{}
					c.Name = "cookie_jwt_test"
					c.Value = tokenStr
					c.Raw = fmt.Sprintf("%s=%s", "cookie_jwt_test", tokenStr)
					return map[string]*http.Cookie{"cookie_jwt_test": &c}
				}(),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			test.jwtR.Output = test.reqOut
			test.jwtR.Key = test.reqKey

			req, err := http.NewRequest(strings.ToUpper(test.method), "/test", nil)
			if err != nil {
				t.Fatal(err)
			}

			reqq := RequestHTTP{
				Response: []ResponseHTTP{
					{
						JWT: &test.jwtR,
					},
				},
			}

			ctx := context.WithValue(req.Context(), sigCtxKey, []byte("Password/Secret"))
			ctx = context.WithValue(ctx, ctxKey("test-1"), &test.jwtC)

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.method, "/test", httpHandler(reqq))
			hdl.ServeHTTP(rec, req.WithContext(ctx))

			if test.want.body != nil {
				have := rec.Body.String()
				if have != *test.want.body {
					t.Errorf("have: %q want: %q", have, *test.want.body)
				}
			}

			if test.want.header != nil {
				if len(test.want.header) != len(rec.Header()) {
					t.Errorf("\nhave: %#v\nwant: %#v", rec.Header(), test.want.header)
				}
				for k, want := range test.want.header {
					have := rec.Header().Values(k)
					if !reflect.DeepEqual(have, want) {
						t.Errorf("have: %#v want: %#v", have, want)
					}
				}
			}

			if test.want.cookies != nil {
				if len(rec.Result().Cookies()) != len(test.want.cookies) {
					t.Errorf("\nhave: %#v\nwant: %#v", rec.Result().Cookies(), test.want.cookies)
				}
				for _, cookie := range rec.Result().Cookies() {
					have := cookie
					want, ok := test.want.cookies[cookie.Name]
					if !ok {
						t.Errorf("no cookie for: %s", cookie.Name)
					}
					if !reflect.DeepEqual(have, want) {
						t.Errorf("\nhave: %#v\nwant: %#v", have, want)
					}
				}
			}
		})
	}
}

func TestProxyHandler(t *testing.T) {

	type testFromProxyHandler struct {
		test func(*testFromProxyHandler, *testing.T, http.Header)
	}

	var tp = &testFromProxyHandler{}

	mux := &http.ServeMux{}
	mux.Handle("/test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tp.test(tp, t, r.Header)
		fmt.Fprintln(w, "I am from the proxy server")
	}))
	pxySvr := httptest.NewServer(mux)
	defer func() { pxySvr.Close() }()

	u, err := url.Parse(pxySvr.URL)
	if err != nil {
		t.Fatal(err)
	}

	var tests = []struct {
		name          string
		method        string
		body          io.Reader
		cfgPxy        configProxy
		req           RequestHTTP
		hasReq        bool // set this true to override
		wantFromProxy func(*testFromProxyHandler, *testing.T, http.Header)
		want          string
	}{
		{
			name:   "basic",
			method: "get",
			cfgPxy: configProxy{
				Name: "test1",
				_url: u,
			},
			req: RequestHTTP{
				Method: "get",
				Response: []ResponseHTTP{
					{
						Status: "test1",
					},
				},
			},
			wantFromProxy: func(_ *testFromProxyHandler, t *testing.T, h http.Header) {

			},
			want: "I am from the proxy server\n",
		},
		{
			name:   "config headers",
			method: "get",
			cfgPxy: configProxy{
				Name: "test1",
				Headers: &headers{
					Data: map[string][]string{
						"x-from-config": {"abc-123"},
					},
				},
				_url: u,
			},
			req: RequestHTTP{
				Method: "get",
				Response: []ResponseHTTP{
					{
						Status: "test1",
					},
				},
			},
			want: "I am from the proxy server\n",
		},
		{
			name:   "request headers",
			method: "get",
			cfgPxy: configProxy{
				Name: "test1",
				_url: u,
			},
			req: RequestHTTP{
				Method: "get",
				Response: []ResponseHTTP{
					{
						Status: "test1",
						Headers: &headers{
							Data: map[string][]string{
								"x-from-request": {"xyz-789"},
							},
						},
					},
				},
			},
			want: "I am from the proxy server\n",
		},
		{
			name:   "both config/request headers",
			method: "get",
			cfgPxy: configProxy{
				Name: "test1",
				Headers: &headers{
					Data: map[string][]string{
						"x-from-config": {"abc-123"},
					},
				},
				_url: u,
			},
			req: RequestHTTP{
				Method: "get",
				Response: []ResponseHTTP{
					{
						Status: "test1",
						Headers: &headers{
							Data: map[string][]string{
								"x-from-request": {"xyz-789"},
							},
						},
					},
				},
			},
			want: "I am from the proxy server\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			var respIdx int
			tp.test = func(_ *testFromProxyHandler, t *testing.T, hdr http.Header) {
				defer func() { respIdx++ }()

				test.hasReq = true
				var cfgHeaders int
				var reqHeaders int
				if test.cfgPxy.Headers != nil && len(test.cfgPxy.Headers.Data) > 0 {
					cfgHeaders = len(test.cfgPxy.Headers.Data)
					for k, ws := range test.cfgPxy.Headers.Data { // have
						hs := hdr.Values(k) // want
						if !reflect.DeepEqual(hs, ws) {
							t.Errorf("[cfg-header] have: %s:%v want: %s:%v", k, hs, k, ws)
						}
					}
				}

				if len(test.req.Response) > respIdx &&
					test.req.Response[respIdx].Headers != nil &&
					len(test.req.Response[respIdx].Headers.Data) > 0 {
					reqHeaders = len(test.req.Response[respIdx].Headers.Data)
					for k, ws := range test.req.Response[respIdx].Headers.Data { // have
						hs := hdr.Values(k) // want
						if !reflect.DeepEqual(hs, ws) {
							t.Errorf("[req-header] have: %s:%v want: %s:%v", k, hs, k, ws)
						}
					}
				}

				hl := len(hdr) - 1 - cfgHeaders - reqHeaders // the one is Accept-Encoding
				wl := 0
				if hl != wl {
					t.Errorf("[header(len)] have: %d want: %d", hl, wl)
				}
			}

			req, err := http.NewRequest(strings.ToUpper(test.method), "/test", test.body)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.WithValue(req.Context(), ctxKey(test.cfgPxy.Name), &test.cfgPxy)

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.req.Method, "/test", httpHandler(test.req))
			hdl.ServeHTTP(rec, req.WithContext(ctx))

			have := rec.Body.String()
			if have != test.want {
				t.Errorf("[body] have: %q want: %q", have, test.want)
			}

			if !test.hasReq {
				t.Errorf("proxy handler not triggered as expected")
			}

		})
	}

}
