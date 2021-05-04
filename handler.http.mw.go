package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	jwtgo "github.com/dgrijalva/jwt-go"
)

// CtxKeyRetries is the context key that holds retry middleware that is
// used when error checking and retrying requests route matches.
const CtxKeyRetries ctxKey = "_retry_"

// checkRetries is middleware that sets the retry context values on a request
// if there are more that on requests available to check.
func checkRetries(v hfsmws) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), CtxKeyRetries, v)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// checkBasicAuth is middleware that preforms a Basic Auth check. Any errors result
// in a 401 wrapped error
func checkBasicAuth(config ConfigHTTP, notfound http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(WriteError(func(w http.ResponseWriter, r *http.Request) error {
			authStrs := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
			if len(authStrs) != 2 {
				return Ext401Error{fmt.Errorf("auth header is not two parts")}
			}

			b, err := base64.StdEncoding.DecodeString(authStrs[1])
			if err != nil {
				return ErrDecodeBase64.F401(err)
			}

			userpass := strings.SplitN(string(b), ":", 2)
			if len(userpass) != 2 {
				return Ext401Error{fmt.Errorf("username/password is not two parts")}
			}

			if userpass[0] != config.BasicAuth.User || userpass[1] != config.BasicAuth.Pass {
				return Ext401Error{fmt.Errorf("bad username/password")}
			}

			if relm := config.BasicAuth.Relm; relm != "" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, relm))
			}
			next.ServeHTTP(w, r)

			return nil
		}))
	}
}

// checkRequestJWT is middleware that checks an incoming JWT auth against values that it should contain
func checkRequestJWT(req RequestHTTP, notfound http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return WriteError(func(w http.ResponseWriter, r *http.Request) error {
			token, err := decodeJWT(w, r, req.JWT)
			if err != nil {
				if !errors.As(err, &WarnError{}) {
					return ErrMarshalJWT.F(err)
				}
			}

			// go through the claims and see if the strings match
			if claims, ok := token.Claims.(jwtgo.MapClaims); ok {
				for k, clav := range claims {
					if reqv, ok := req.JWT.KeyVals[k]; ok {
						if v1, ok := clav.(string); ok {
							v2, _ := reqv.Expr.Value(nil)
							if v1 != v2.AsString() {
								return ErrInvalidJWTClaim
							}
						}
					}
				}
			}

			ctx := context.WithValue(r.Context(), CtxKeyJWTToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))

			return nil
		})
	}
}

// checkRequestHeader checks incoming header values against values that it should contain
func checkRequestHeader(req RequestHTTP, _nf http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return WriteError(func(w http.ResponseWriter, r *http.Request) error {
			for k, vals := range req.Headers.Data {
				values := r.Header.Values(k)
				chk := len(vals)
				if chk != len(values) {
					return ErrFilterFailed.F404("header", "unequal lengths")
				}

				// check that all the values are the same or a "*"
				for _, val := range vals {
					v1 := val.AsString()
					for _, v2 := range values {
						if v1 == "*" || v1 == v2 {
							chk--
						}
					}
				}

				// if we've found them all then we'll be at 0, otherwise...
				if chk != 0 {
					return ErrFilterFailed.F404("header", "did not find a value")
				}

				next.ServeHTTP(w, r)
			}
			return nil
		})
	}
}

// checkRequestJWT checks incoming post against values that it should contain
func checkRequestPost(req RequestHTTP, notfound http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return WriteError(func(w http.ResponseWriter, r *http.Request) error {
			err := r.ParseForm()
			if err != nil {
				return ErrParseForm.F(err)
			}

			for k, v := range req.Posted {
				if v == "*" {
					continue
				}
				if v != r.PostFormValue(k) {
					notfound(w, r)
					return nil
				}
			}

			next.ServeHTTP(w, r)
			return nil
		})
	}
}
