package main

import (
	"fmt"
	"net/http"
	"strings"
)

// corsHandler handles checking CORS options and
// making sure they are valid before continuing to
// process a HTTP request
func corsHandler(cors *routeCORS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		log.Println("[cors] sending back headers ...")
		if cors == nil {
			log.Println("[cors] skipping ...")
			return
		}

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
