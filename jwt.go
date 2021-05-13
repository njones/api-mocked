package main

import (
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
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// context keys for JWT token information that's stored
// in a context during a request
const (
	CtxKeyJWTToken  ctxKey = "_jwt_token_" // the parsed JWT token
	CtxKeySignature ctxKey = "_sig_"       // the secret bytes (HMAC bytes or RSA bytes)
)

// jwtSigMap a map of supported JWT signature types with the methods
// needed to support signing/validating a JWT token
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

// useJWT sets up a JWT token based off the configuration supplied
// by the ConfigHTTP options
func useJWT(server ConfigHTTP) interface{} {
	var sigKey interface{}

	log.Printf("[jwt] %q setup (algo: %s) ...", server.Name, server.JWT.Alg)
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
		if val, dia := server.JWT.Key.Expr.Value(&bodyEvalCtx); !dia.HasErrors() {
			signKey, err := jwtgo.ParseECPrivateKeyFromPEM([]byte(val.AsString()))
			if err != nil {
				ErrEncodeJWTResponse.F(err)
			}
			sigKey = signKey
		} else {
			panic(fmt.Errorf("[jwt] getting RS key: %v", dia))
		}
	case "ps":
		if val, dia := server.JWT.Key.Expr.Value(&bodyEvalCtx); !dia.HasErrors() {
			signKey, err := jwtgo.ParseRSAPrivateKeyFromPEM([]byte(val.AsString()))
			if err != nil {
				ErrEncodeJWTResponse.F(err)
			}
			sigKey = signKey
		} else {
			panic(fmt.Errorf("[jwt] getting RS key: %v", dia))
		}
	case "ed":
		panic("[jwt] using a EdDSA key is unsupported")
	}

	return sigKey
}

// decodeJWT is called during the HTTP request to decode and validate a
// JWT token. This takes the writer to set headers if the config determines
// it takes the request to pull out headers, cookies or query values if
// deteremined by config. It returns any errors and the token marked as valid
// or invalid.
func decodeJWT(w http.ResponseWriter, r *http.Request, reqJWT *requestJWT) (token *jwtgo.Token, err error) {
	if reqJWT == nil {
		return token, nil
	}

	log.Printf("[jwt] decode %s ...", reqJWT.Input)

	var jwtStr string
	switch reqJWT.Input {
	case "auth":
		data := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
		if len(data) == 2 && reqJWT.Key == strings.ToLower(data[0]) {
			jwtStr = data[1]
		}
	case "cookie":
		cookie, err := r.Cookie(reqJWT.Key)
		if err != nil {
			return nil, err
		}
		jwtStr = cookie.Value
	case "header":
		jwtStr = r.Header.Get(reqJWT.Key)
	case "query":
		jwtStr = r.URL.Query().Get(reqJWT.Key)
	default:
		return nil, ErrInvalidJWTLoc.F("input")
	}

	if jwtStr != "" {
		log.Println("[jwt] parsing JWT token ...")
		claims := jwtgo.MapClaims{}
		token, err = jwtgo.ParseWithClaims(jwtStr, claims, func(token *jwtgo.Token) (interface{}, error) {
			key := r.Context().Value(CtxKeySignature)
			switch k := key.(type) {
			case []byte:
				return k, nil
			case *rsa.PrivateKey:
				return k, nil
			}
			return nil, fmt.Errorf("invalid key")
		})

		if err != nil {
			// the following test should follow this logic:
			// if validate is nil (not set) then return any errors
			// OR if validate is false then return any errors
			if reqJWT.Validate == nil || !*reqJWT.Validate {
				return token, err // this only returns validation errors
			}
		}

		if reqJWT.Validate != nil && *reqJWT.Validate {
			log.Println("[jwt] setting validation header ...")

			v := "invalid"
			if token.Valid {
				v = "valid"
			}
			w.Header().Set("x-jwt-validation", v)
			if err != nil {
				err = WarnError{err}
			}
		}
	}

	return token, err
}

// marshalJWT takes a JWT response struct and returns a JWT string with all
// of the values passed though HCL contexts
func marshalJWT(cfgJWT *configJWT, respJWT *responseJWT, key interface{}) (string, error) {
	if cfgJWT != nil {
		switch k := key.(type) {
		case []byte:
			respJWT.Payload["$._internal."+cfgJWT.Name+".key"] = string(key.([]byte))
		case *rsa.PrivateKey:
			b := pem.EncodeToMemory(
				&pem.Block{
					Type:  "RSA PRIVATE KEY",
					Bytes: x509.MarshalPKCS1PrivateKey(k),
				},
			)
			respJWT.Payload["$._internal."+cfgJWT.Name+".key"] = string(b)
		}
	}

	if algo, ok := jwtSigMap[cfgJWT.Alg]; ok {
		token := jwtgo.NewWithClaims(algo, respJWT)
		return token.SignedString(key)
	}

	return "", fmt.Errorf("no algo found")
}

// Makes sure that the claims are valid ...
// this is taken from: https://github.com/dgrijalva/jwt-go/blob/dc14462fd58732591c7fa58cc8496d6824316a82/claims.go

// useImpliedZeroIndex returns a 0-index for variables that require an index, but don't have one specified
// this to to make variables natural to use.
func useImpliedZeroIndex(a *hcl.Attribute) {
	// this will set the index to 0 if a variable is ${post.<value>} ... it will make things right
	// I tried serveral other ways, but this is the one that worked
	vars := a.Expr.Variables()
	for _, v1 := range vars {
		switch vE := a.Expr.(type) {
		case *hclsyntax.TemplateWrapExpr:
			if scope, ok := vE.Wrapped.(*hclsyntax.ScopeTraversalExpr); ok && len(v1) == 2 {
				if root, ok := scope.Traversal[0].(hcl.TraverseRoot); ok {
					if root.Name == "post" {
						k1 := hcl.TraverseIndex{Key: cty.NumberIntVal(0)}
						a.Expr.(*hclsyntax.TemplateWrapExpr).Wrapped.(*hclsyntax.ScopeTraversalExpr).Traversal = append(a.Expr.(*hclsyntax.TemplateWrapExpr).Wrapped.(*hclsyntax.ScopeTraversalExpr).Traversal, k1)
					}
				}
			}
		}
	}
}

// MarshalJSON provides a marshal state for the request JSON. this builds
// the string manually from scratch and resolves all context issues.
func (r *responseJWT) MarshalJSON() (b []byte, err error) {
	var addComma bool
	b = append(b, '{')

	val := reflect.ValueOf(r).Elem()
	for i := 0; i < val.Type().NumField(); i++ {
		if !val.Field(i).CanSet() {
			continue
		}
		if a, ok := val.Field(i).Interface().(*hcl.Attribute); ok && a != nil {
			useImpliedZeroIndex(a)
			if addComma {
				b = append(b, ","...)
			}
			name := val.Type().Field(i).Tag.Get("json")
			val, _ := a.Expr.Value(r._ctx)
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

// Valid statisfys the `*jwt.Claims` interface so we can map values
// to this struct as raw JWT token data
func (r *responseJWT) Valid() error {
	vErr := new(jwtgo.ValidationError)
	now := jwtgo.TimeFunc().Unix()

	// The claims below are optional, by default, so if they are set to the
	// default value in Go, let's not fail the verification for them.
	if !r.VerifyExpiresAt(now, false) {
		useImpliedZeroIndex(r.Expiration)
		num, _ := r.Expiration.Expr.Value(r._ctx)
		expiresAt, _ := num.AsBigFloat().Int64()
		delta := time.Unix(now, 0).Sub(time.Unix(expiresAt, 0))
		vErr.Inner = fmt.Errorf("token is expired by %v", delta)
		vErr.Errors |= jwtgo.ValidationErrorExpired
	}

	if !r.VerifyIssuedAt(now, false) {
		vErr.Inner = fmt.Errorf("token used before issued")
		vErr.Errors |= jwtgo.ValidationErrorIssuedAt
	}

	if !r.VerifyNotBefore(now, false) {
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
func (r *responseJWT) VerifyAudience(cmp string, req bool) bool {
	aud, _ := r.Audience.Expr.Value(nil)
	return verifyAud(aud.AsString(), cmp, req)
}

// Compares the exp claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *responseJWT) VerifyExpiresAt(cmp int64, req bool) bool {
	useImpliedZeroIndex(r.Expiration)
	num, _ := r.Expiration.Expr.Value(r._ctx)
	exp, _ := num.AsBigFloat().Int64()
	return verifyExp(exp, cmp, req)
}

// Compares the iat claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *responseJWT) VerifyIssuedAt(cmp int64, req bool) bool {
	useImpliedZeroIndex(r.IssuedAt)
	num, _ := r.IssuedAt.Expr.Value(r._ctx)
	iat, _ := num.AsBigFloat().Int64()
	return verifyIat(iat, cmp, req)
}

// Compares the iss claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *responseJWT) VerifyIssuer(cmp string, req bool) bool {
	iss, _ := r.Issuers.Expr.Value(nil)
	return verifyIss(iss.AsString(), cmp, req)
}

// Compares the nbf claim against cmp.
// If required is false, this method will return true if the value matches or is unset
func (r *responseJWT) VerifyNotBefore(cmp int64, req bool) bool {
	useImpliedZeroIndex(r.NotBefore)
	num, _ := r.NotBefore.Expr.Value(r._ctx)
	nbf, _ := num.AsBigFloat().Int64()
	return verifyNbf(nbf, cmp, req)
}

// ----- helpers (picked up from the JWT library)

func verifyAud(aud string, cmp string, required bool) bool {
	if aud == "" {
		return !required
	}
	if subtle.ConstantTimeCompare([]byte(aud), []byte(cmp)) != 0 {
		return true
	}
	return false
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
	}
	return false
}

func verifyNbf(nbf int64, now int64, required bool) bool {
	if nbf == 0 {
		return !required
	}
	return now >= nbf
}
