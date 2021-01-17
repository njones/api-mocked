package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

func TestRequestHandler_Headers(t *testing.T) {

	type wants struct {
		statusCode int
		body       string
	}

	var tests = []struct {
		name    string
		headers http.Header
		req     request
		want    wants
	}{
		{
			name:    "normal",
			headers: http.Header{"A": {"b"}},
			req: request{
				Headers: &headers{
					Data: map[string][]string{
						"a": {"b"},
					},
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr("Hello, World"),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name:    "no header",
			headers: http.Header{"A": {"b"}},
			req: request{
				Headers: &headers{
					Data: map[string][]string{
						"c": {"d"},
					},
				},
				Response: []response{
					{
						Status: "200",
						Body:   nil,
					},
				},
			},
			want: wants{
				statusCode: 404,
				body:       "404 page not found\n",
			},
		},
		{
			name:    "header used in template",
			headers: http.Header{"A": {"World"}},
			req: request{
				Headers: &headers{
					Data: map[string][]string{
						"A": {"d"},
					},
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, {{ Header "a" }}`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/test", nil)
			if err != nil {
				t.Fatal(err)
			}

			if test.headers != nil {
				req.Header = test.headers
			}

			rec := httptest.NewRecorder()
			handler := httpHandler(test.req)
			handler.ServeHTTP(rec, req)

			if rec.Code != test.want.statusCode {
				t.Errorf("have: %d want: %d", rec.Code, test.want.statusCode)
			}

			if rec.Body.String() != test.want.body {
				t.Errorf("\nhave: %q\nwant: %q", rec.Body.String(), test.want.body)
			}
		})
	}
}

var attr = func(s string) *hcl.Attribute {
	return &hcl.Attribute{Name: "body", Expr: hcl.StaticExpr(cty.StringVal(s), hcl.Range{Start: hcl.InitialPos, End: hcl.Pos{Byte: 0, Line: 1, Column: len(s)}})}
}

func TestRequestHandler_URLParams(t *testing.T) {

	type wants struct {
		statusCode int
		body       string
	}

	var tests = []struct {
		name   string
		method string
		url    string // the URL to simulate

		path string // the path used in the config
		req  request
		want wants
	}{
		{
			name:   "normal",
			method: "get",
			url:    "/this/is/standard",
			path:   "/this/is/standard",
			req: request{
				Response: []response{
					{
						Status: "200",
						Body:   attr("Hello, World"),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name:   "url param",
			method: "get",
			url:    "/this/is/different",
			path:   "/this/is/standard",
			req: request{
				Response: []response{
					{
						Status: "200",
						Body:   nil,
					},
				},
			},
			want: wants{
				statusCode: 404,
				body:       "404 page not found\n",
			},
		},
		{
			name:   "url param",
			method: "get",
			url:    "/this/is/World",
			path:   "/this/is/{id}",
			req: request{
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, {{ UrlParam "id" }}`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name:   "query param",
			method: "get",
			url:    "/this/is/standard?id=World",
			path:   "/this/is/standard",
			req: request{
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, {{ QueryParam "id" }}`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name:   "query param doesnt exist",
			method: "get",
			url:    "/this/is/standard?hello=World",
			path:   "/this/is/standard",
			req: request{
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, {{ QueryParam "id" }}`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, ",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", test.url, nil)
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			hdl := chi.NewRouter()
			hdl.Method(test.method, test.path, httpHandler(test.req))
			hdl.ServeHTTP(rec, req)

			if rec.Code != test.want.statusCode {
				t.Errorf("have: %d want: %d", rec.Code, test.want.statusCode)
			}

			if rec.Body.String() != test.want.body {
				t.Errorf("\nhave: %q\nwant: %q", rec.Body.String(), test.want.body)
			}
		})
	}
}

func TestRequestHandler_JWT(t *testing.T) {

	var True = true

	type wants struct {
		statusCode int
		body       string
		validation string
	}

	type auth struct {
		kind string
		key  string
	}

	var tests = []struct {
		name   string
		auth   auth
		secret []byte

		req  request
		want wants
	}{
		{
			name: "normal",
			auth: auth{
				kind: "auth",
				key:  "bearer",
			},
			req: request{
				JWT: &jwtRequest{
					Input: "auth",
					Key:   "bearer",
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr("Hello, World"),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name: "auth header",
			auth: auth{
				kind: "auth",
				key:  "bearer",
			},
			req: request{
				JWT: &jwtRequest{
					Input: "auth",
					Key:   "bearer",
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, {{ JwtField "hello" }}`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
			},
		},
		{
			name: "auth cookie",
			auth: auth{
				kind: "cookie",
				key:  "session",
			},
			req: request{
				JWT: &jwtRequest{
					Input:    "cookie",
					Key:      "session",
					Validate: &True,
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, World`),
					},
				},
			},
			want: wants{
				statusCode: 200,
				body:       "Hello, World",
				validation: "valid",
			},
		},
		{
			name: "normal invalid",
			auth: auth{
				kind: "auth",
				key:  "bearer",
			},
			req: request{
				JWT: &jwtRequest{
					Input:    "auth",
					Key:      "bearer",
					Validate: &True,
				},
				Response: []response{
					{
						Status: "200",
						Body:   attr(`Hello, World`),
					},
				},
			},
			secret: []byte("does not compute"),
			want: wants{
				statusCode: 500,
				body:       "Internal server error\n",
				validation: "invalid",
			},
		},
	}

	type Claims struct {
		Hello string `json:"hello"`
		jwt.StandardClaims
	}

	expirationTime := time.Now().Add(5 * time.Minute)
	// Create the JWT claims, which includes the username and expiry time
	claims := &Claims{
		Hello: "World",
		StandardClaims: jwt.StandardClaims{
			// In JWT, the expiry time is expressed as unix milliseconds
			ExpiresAt: expirationTime.Unix(),
		},
	}

	secret := []byte("my_secret_key")
	// Declare the token with the algorithm used for signing, and the claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Create the JWT string
	tokenString, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/test-jwt", nil)
			if err != nil {
				t.Fatal(err)
			}

			switch test.auth.kind {
			case "auth":
				req.Header.Set("Authorization", test.auth.key+" "+tokenString)
			case "cookie":
				cookie := http.Cookie{
					Name:  test.auth.key,
					Value: tokenString,
				}
				req.AddCookie(&cookie)
			case "header":
				req.Header.Set(test.auth.key, tokenString)
			}

			var secr3t = test.secret
			if test.secret == nil {
				secr3t = secret
			}
			ctx := context.WithValue(req.Context(), sigCtxKey, secr3t)

			rec := httptest.NewRecorder()
			hdl := httpHandler(test.req)
			hdl.ServeHTTP(rec, req.WithContext(ctx))

			if rec.Code != test.want.statusCode {
				t.Errorf("have: %d want: %d", rec.Code, test.want.statusCode)
			}

			if rec.Body.String() != test.want.body {
				t.Errorf("\nhave: %q\nwant: %q", rec.Body.String(), test.want.body)
			}

			if test.req.JWT.Validate != nil && *test.req.JWT.Validate {
				have := rec.Header().Get("x-jwt-validation")
				if have != test.want.validation {
					t.Errorf("\nhave: %q\nwant: %q", have, test.want.validation)
				}
			}
		})
	}
}
