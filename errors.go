package main

import (
	"errors"
	"fmt"
	"net/http"
)

// all of the system errors
const (
	ErrHeaderMatchNotFound StdError = "failed match header not found"
	ErrEncodeJWTResponse   StdError = "failed encoding JWT: %v"
	ErrDecodeJWTResponse   StdError = "failed decoding JWT: %v"
	ErrDecodeBase64        StdError = "failed decoding base64 content: %v"
	ErrBadHCLExpression    StdError = "failed HCL eval of expression: %v"
	ErrTemplateParse       StdError = "failed parsing template: %v"
	ErrParseForm           StdError = "failed parsing the form: %v"
	ErrParseInt            StdError = "failed parsing int to string: %v"
	ErrParseDuration       StdError = "failed parsing time duration: %v"
	ErrBigIntCreation      StdError = "failed creating a big number"
	ErrGetNetInterface     StdError = "failed aquiring a network interface: %v"
	ErrGetNetAddr          StdError = "failed network address: %v"
	ErrParseTimeFmt        StdError = "failed parsing time: %v"
	ErrParseCACert         StdError = "failed parsing the ca cert: %v"
	ErrGenKey              StdError = "failed generating key: %v"
	ErrCreateX590Cert      StdError = "failed creating a x509 certificate: %v"
	ErrLoadX509            StdError = "failed loading a x509 ca key pair: %v"
	ErrCreateTLSCert       StdError = "failed creating tls cert: %v"
	ErrMarshalPrivKey      StdError = "failed marshaling private key: %v"
	ErrMarshalPubKey       StdError = "failed marshaling public key: %v"
	ErrOrderIndexParse     StdError = "failed parsing the order index to a valid number: %v"
	ErrReadRequestBody     StdError = "failed reading the request body: %v"
	ErrMarshalJWT          StdError = "failed parsing the JWT: %v"

	ErrJWTConfigurationNotFound StdError = "JWT configuration not found in the context"
	ErrInvalidJWTClaim          StdError = "invalid JWT claim"
	ErrInvalidJWTLoc            StdError = "invalid JWT %s location"
	ErrInvalidAuth              StdError = "invalid authorization"
)

// StdError is a standard error.
type StdError string

func (e StdError) Error() string {
	return string(e)
}

// F accepts errors with parameters and wraps
// parameters that use a `%w`
func (e StdError) F(v ...interface{}) error {
	var hasErr, hasNil bool
	for _, vv := range v {
		switch err := vv.(type) {
		case error:
			if err == nil {
				return nil
			}
			hasErr = true
		case nil:
			hasNil = true
		}
	}

	if hasNil && !hasErr {
		return nil
	}

	err := ExtError{
		err: fmt.Errorf("%w", e),
		v:   v,
	}

	return err
}

func (e StdError) F400(v ...interface{}) error {
	return Ext400Error{e.F(v...).(ExtError)}
}

func (e StdError) F401(v ...interface{}) error {
	return Ext401Error{e.F(v...).(ExtError)}
}

func (e StdError) F404(v ...interface{}) error {
	return Ext404Error{e.F(v...).(ExtError)}
}

// ExtError is an error with parameters.
type ExtError struct {
	err error
	v   []interface{}
}

// Error returns error string.
func (e ExtError) Error() string {
	return fmt.Sprintf(e.err.Error(), e.v...)
}

// Unwrap returns unwrapped error.
func (e ExtError) Unwrap() error {
	return errors.Unwrap(e.err)
}

// erorr handling ideas from: https://thingsthatkeepmeupatnight.dev/posts/golang-http-handler-errors/
// thanks Ville Hakulinen for the code examples and good ideas

type HandlerError interface {
	// ResponseError writes an error message to w. If it doesn't know what to
	// respond, it returns false.
	ErrorResponseWriter(w http.ResponseWriter, r *http.Request) bool
}

type HandlerE = func(w http.ResponseWriter, r *http.Request) error

func WriteError(h HandlerE) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			if he, ok := err.(HandlerError); ok {
				if he.ErrorResponseWriter(w, r) {
					return
				}
			}

			log.Printf("invalid handler error: %v", err)
			http.Error(w, "Internal server error", 500)
		}
	}
}

type Ext400Error struct{ error }

func (e Ext400Error) ErrorResponseWriter(w http.ResponseWriter, r *http.Request) bool {
	http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	log.Error(e.error)
	return true
}

type Ext401Error struct{ error }

func (e Ext401Error) ErrorResponseWriter(w http.ResponseWriter, r *http.Request) bool {
	http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
	log.Error(e.error)
	return true
}

type Ext404Error struct{ error }

func (e Ext404Error) ErrorResponseWriter(w http.ResponseWriter, r *http.Request) bool {
	http.Error(w, "404 page not found", http.StatusNotFound)
	log.Error(e.error)
	return true
}

type WarnError struct{ error }
