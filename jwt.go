package main

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

var sigCtxKey = ctxKey("_sig_")

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

func useJWT(mw *chi.Mux, server serverConfig) {
	if server.JWT == nil {
		return
	}

	log.Printf("[jwt] %q setup (algo: %s) ...", server.Name, server.JWT.Alg)
	var sigKey interface{}
	switch strings.ToLower(server.JWT.Alg)[:2] {
	case "hs":
		if val, dia := server.JWT.Secret.Expr.Value(&fileEvalCtx); !dia.HasErrors() {
			sigKey = []byte(val.AsString())
		} else {
			panic(fmt.Errorf("[jwt] getting HS secret: %v", dia))
		}
	case "rs":
		if val, dia := server.JWT.Key.Expr.Value(&bodyEvalCtx); !dia.HasErrors() {
			signKey, err := jwtgo.ParseRSAPrivateKeyFromPEM([]byte(val.AsString()))
			if err != nil {
				ErrEncodeJWTResponse.F(err)
			}
			sigKey = signKey
		} else {
			panic(fmt.Errorf("[jwt] getting RS key: %v", dia))
		}
	case "es":
	case "ps":
	case "ed":
	}

	log.Printf("[jwt] %q middleware added (%p) ...", server.Name, mw)
	mw.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxKey(server.JWT.Name), server.JWT)
			ctx = context.WithValue(ctx, sigCtxKey, sigKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})

}

func decodeJWT(w http.ResponseWriter, r *http.Request, reqJWT *jwtRequest) (token *jwtgo.Token, err error) {
	if reqJWT == nil {
		return token, nil
	}

	log.Printf("[jwt] decode %s ...", reqJWT.Input)

	var jwtStr string
	switch reqJWT.Input {
	case "header":
		jwtStr = r.Header.Get(reqJWT.Key)
	case "cookie":
		cookie, err := r.Cookie(reqJWT.Key)
		if err != nil {
			return nil, err
		}
		jwtStr = cookie.Value
	case "auth":
		data := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
		if len(data) == 2 && reqJWT.Key == strings.ToLower(data[0]) {
			jwtStr = data[1]
		}
	default:
		return nil, ErrInvalidJWTLoc.F("input")
	}

	if jwtStr != "" {
		log.Println("[jwt] parsing JWT token ...")
		claims := jwtgo.MapClaims{}
		token, err = jwtgo.ParseWithClaims(jwtStr, claims, func(token *jwtgo.Token) (interface{}, error) {
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
			return token, err
		}

		if reqJWT.Validate {
			log.Println("[jwt] setting validation header ...")

			v := "invalid"
			if token.Valid {
				v = "valid"
			}
			w.Header().Set("x-jwt-validation", v)
		}
	}

	return token, err
}

func encodeJWT(w http.ResponseWriter, claims *jwtResponse, cfg *jwtConfig, key interface{}) error {
	if claims == nil {
		return nil
	}

	log.Printf("[jwt] encode %s ...", claims.Output)

	// add the validation key to the token (so this can't be used for production)
	if cfg != nil {
		switch k := key.(type) {
		case []byte:
			claims.Payload["$._internal."+cfg.Name+".key"] = string(key.([]byte))
		case *rsa.PrivateKey:
			b := pem.EncodeToMemory(
				&pem.Block{
					Type:  "RSA PRIVATE KEY",
					Bytes: x509.MarshalPKCS1PrivateKey(k),
				},
			)
			claims.Payload["$._internal."+cfg.Name+".key"] = string(b)
		}
	}

	log.Println("[jwt] mint token ...")
	if algo, ok := jwtSigMap[cfg.Alg]; ok {
		token := jwtgo.NewWithClaims(algo, claims)
		tokStr, err := token.SignedString(key)
		if err != nil {
			return err
		}

		switch claims.Output {
		case "header":
			w.Header().Add(claims.Key, tokStr)
		case "cookie":
			cookie := http.Cookie{
				Name:  claims.Key,
				Value: tokStr,
			}
			http.SetCookie(w, &cookie)
		default:
			fmt.Fprint(w, tokStr)
		}
	}

	return nil
}

// Makes sure that the claims are valid ...
// this is taken from: https://github.com/dgrijalva/jwt-go/blob/dc14462fd58732591c7fa58cc8496d6824316a82/claims.go

func (r *jwtResponse) MarshalJSON() (b []byte, err error) {
	var addComma bool
	b = append(b, '{')

	val := reflect.ValueOf(r).Elem()
	for i := 0; i < val.Type().NumField(); i++ {
		if !val.Field(i).CanSet() {
			continue
		}
		if a, ok := val.Field(i).Interface().(*hcl.Attribute); ok && a != nil {
			if addComma {
				b = append(b, ","...)
			}
			name := val.Type().Field(i).Tag.Get("json")
			val, _ := a.Expr.Value(jwtEvalCtx(r.Name, r.Key, r._hclVarMap))
			b = append(b, fmt.Sprintf("%q:", name)...)
			switch vt := val.Type(); vt {
			case cty.String:
				b = append(b, fmt.Sprintf("%q", val.AsString())...)
			case cty.Number:
				num, _ := val.AsBigFloat().Int64()
				b = append(b, fmt.Sprintf("%d", num)...)
			case cty.Bool:
				b = append(b, fmt.Sprintf("%t", val.True())...)
			default:
				log.Printf("TYPE: %#v", vt)
				b = append(b, `""`...) // just let it be empty
			}
			addComma = true
		}
		if val.Field(i).Kind() == reflect.Slice {
			if a := val.Field(i).Interface(); a != nil && !val.Field(i).IsNil() {
				if addComma {
					b = append(b, ","...)
				}
				name := val.Type().Field(i).Tag.Get("json")
				bs, _ := json.Marshal(val.Field(i).Interface())
				b = append(b, fmt.Sprintf("%q: %s", name, bs)...)
				addComma = true
			}
		}
		if a, ok := val.Field(i).Interface().(map[string]string); ok && len(a) > 0 {
			if addComma {
				b = append(b, ","...)
			}
			bm, _ := json.Marshal(a)
			b = append(b, bm[1:len(bm)-1]...)
			addComma = true
		}
	}

	return append(b, '}'), nil
}

func (r *jwtResponse) Valid() error {
	vErr := new(jwtgo.ValidationError)
	now := jwtgo.TimeFunc().Unix()

	// The claims below are optional, by default, so if they are set to the
	// default value in Go, let's not fail the verification for them.
	if r.VerifyExpiresAt(now, false) == false {
		num, _ := r.Expiration.Expr.Value(jwtEvalCtx(r.Name, r.Key, r._hclVarMap))
		expiresAt, _ := num.AsBigFloat().Int64()
		delta := time.Unix(now, 0).Sub(time.Unix(expiresAt, 0))
		vErr.Inner = fmt.Errorf("token is expired by %v", delta)
		vErr.Errors |= jwtgo.ValidationErrorExpired
	}

	if r.VerifyIssuedAt(now, false) == false {
		vErr.Inner = fmt.Errorf("Token used before issued")
		vErr.Errors |= jwtgo.ValidationErrorIssuedAt
	}

	if r.VerifyNotBefore(now, false) == false {
		vErr.Inner = fmt.Errorf("token is not valid yet")
		vErr.Errors |= jwtgo.ValidationErrorNotValidYet
	}

	if vErr.Errors == 0 {
		return nil
	}

	return vErr
}

// Compares the aud claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *jwtResponse) VerifyAudience(cmp string, req bool) bool {
	aud, _ := r.Audience.Expr.Value(nil)
	return verifyAud(aud.AsString(), cmp, req)
}

// Compares the exp claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *jwtResponse) VerifyExpiresAt(cmp int64, req bool) bool {
	num, _ := r.Expiration.Expr.Value(jwtEvalCtx(r.Name, r.Key, r._hclVarMap))
	exp, _ := num.AsBigFloat().Int64()
	return verifyExp(exp, cmp, req)
}

// Compares the iat claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *jwtResponse) VerifyIssuedAt(cmp int64, req bool) bool {
	num, _ := r.IssuedAt.Expr.Value(jwtEvalCtx(r.Name, r.Key, r._hclVarMap))
	iat, _ := num.AsBigFloat().Int64()
	return verifyIat(iat, cmp, req)
}

// Compares the iss claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *jwtResponse) VerifyIssuer(cmp string, req bool) bool {
	iss, _ := r.Issuers.Expr.Value(nil)
	return verifyIss(iss.AsString(), cmp, req)
}

// Compares the nbf claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *jwtResponse) VerifyNotBefore(cmp int64, req bool) bool {
	num, _ := r.NotBefore.Expr.Value(jwtEvalCtx(r.Name, r.Key, r._hclVarMap))
	nbf, _ := num.AsBigFloat().Int64()
	return verifyNbf(nbf, cmp, req)
}

// ----- helpers

func verifyAud(aud string, cmp string, required bool) bool {
	if aud == "" {
		return !required
	}
	if subtle.ConstantTimeCompare([]byte(aud), []byte(cmp)) != 0 {
		return true
	} else {
		return false
	}
}

func verifyExp(exp int64, now int64, required bool) bool {
	if exp == 0 {
		return !required
	}
	return now <= exp
}

func verifyIat(iat int64, now int64, required bool) bool {
	if iat == 0 {
		return !required
	}
	return now >= iat
}

func verifyIss(iss string, cmp string, required bool) bool {
	if iss == "" {
		return !required
	}
	if subtle.ConstantTimeCompare([]byte(iss), []byte(cmp)) != 0 {
		return true
	} else {
		return false
	}
}

func verifyNbf(nbf int64, now int64, required bool) bool {
	if nbf == 0 {
		return !required
	}
	return now >= nbf
}
