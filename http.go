package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	conf "plugins/config"
	requ "plugins/request"
	resp "plugins/response"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi"
	"github.com/hashicorp/hcl/v2"
)

// ctxKey is the type that is used to wrap context.Context keys (so they are not plain strings)
type ctxKey string

// CtxKeyServerName is the context key that holds name of the server that is supplying the request
const CtxKeyServerName ctxKey = "_server_name_"

// hfsmws HandlerFunc's and MiddleWare's struct, that is passed to the context when there are
// multiple requests in a path. This is so that if a response inside of a path doesn't match
// then you can check others.
type hfsmws struct {
	hfs   []http.HandlerFunc
	mws   []chi.Middlewares
	track int
}
type (
	// PrePluginHTTP runs to add plugins Middleware before any other middleware is added
	// Pass in the route and the Body (which should) usually be the `Plugins` field in a
	// struct. Any Request HTTP information needs to be copied over to the `requ.HTTP`
	// struct (as this breaks cicrular dependencies).
	PrePluginHTTP interface {
		PreMiddlewareHTTP(string, hcl.Body, requ.HTTP) (func(http.Handler) http.Handler, bool)
	}

	// PostPluginHTTP runs to add plugins Middleware after any other middleware is added,
	// but before the CORS and Proxy middleware. Pass in the route and the Body (which should)
	// usually be the `Plugins` field in a struct. Any Request HTTP information needs to be
	// copied over to the `requ.HTTP` struct (as this breaks cicrular dependencies).
	PostPluginHTTP interface {
		PostMiddlewareHTTP(string, hcl.Body, requ.HTTP) (MiddlewareHTTP, bool)
	}
)

type (
	// PrePluginConfigHTTP runs any plugin configurations before the builtin
	// configuration options. Pass in the Body (which should)
	// usually be the `Plugins` field in a struct. Any Request HTTP information
	// needs to be copied over to the `requ.HTTP` struct (as this breaks any
	// cicrular dependencies).
	PrePluginConfigHTTP interface {
		PreConfigHTTP(hcl.Body, conf.HTTP) error
	}

	// PostPluginConfigHTTP runs any plugin configurations after the builtin
	// configuration options. Pass in the Body (which should) usually be the
	// `Plugins` field in a struct. Any Request HTTP information needs to
	// be copied over to the `requ.HTTP` struct (as this breaks any cicrular
	// dependencies).
	PostPluginConfigHTTP interface {
		PostConfigHTTP(hcl.Body, conf.HTTP) error
	}
)

type (
	// PrePluginRequestHTTP runs any plugin configurations during
	// the start of a HTTP request setup.
	PrePluginRequestHTTP interface {
		PreRequestHTTP(hcl.Body, requ.HTTP) error
	}

	// PostPluginRequestHTTP runs any plugin configurations during
	// the end of a HTTP request setup.
	PostPluginRequestHTTP interface {
		PostRequestHTTP(hcl.Body, requ.HTTP) error
	}
)

type (
	// PrePluginResponseHTTP runs any plugin configurations during
	// the start of a HTTP response setup.
	PrePluginResponseHTTP interface {
		PreResponseHTTP(hcl.Body, resp.HTTP) error
	}

	// PostPluginResponseHTTP runs any plugin configurations during
	// the start of a HTTP response setup.
	PostPluginResponseHTTP interface {
		PostResponseHTTP(hcl.Body, resp.HTTP) error
	}
)

// plugins is a global map that holds all of the plugins.
// both GO plugin, and builtin plugins
var plugins = make(map[string]Plugin)

// _http sets up HTTP servers and services that rely on HTTP
// this is a blocking function that ends up serving how many
// HTTP servers should be created
func _http(config *Config) chan struct{} {

	ro := chi.NewRouter() // routes
	mw := chi.NewRouter() // middleware

	mw.Use(log.HTTPMiddleware)
	for _, route := range config.Routes {

		// setup CORS if needed...
		var corsMidware MiddlewareHTTP
		if route.CORS != nil {
			block := *route.CORS // copy them here...
			corsMidware = func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					corsHandler(&block).ServeHTTP(w, r)
					next.ServeHTTP(w, r)
				})
			}
			log.Printf("[http] OPTIONS %s added ...", route.Path)
			ro.With(corsMidware).MethodFunc("options", route.Path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		}

		is := make(map[string]int)
		for _, v := range route.Request {
			for _, method := range strings.Split(v.Method, "|") {
				is[strings.ToUpper(method)]++
			}
		}

		// collect multiple response structs that
		// can be matched against later
		multiResponse := make(map[string]hfsmws)
		for k, i := range is {
			multiResponse[k] = hfsmws{hfs: make([]http.HandlerFunc, i), mws: make([]chi.Middlewares, i)}
		}

		// add http response routes
		for i, req := range route.Request {
			for _, method := range strings.Split(req.Method, "|") {
				method = strings.ToUpper(strings.TrimSpace(method))

				var midware chi.Middlewares

				// add any method middleware
				// add any plugin pre middleware
				for k, plugin := range plugins {
					if plug, ok := plugin.(PrePluginHTTP); ok {
						requHTTP := requ.HTTP{Method: req.Method, Ticker: req.Ticker, Order: req.Order, Delay: req.Delay}
						if hdlr, ok := plug.PreMiddlewareHTTP(route.Path, req.Plugins, requHTTP); ok {
							log.Printf("[http][%s][pre] %s middleware added ...", k, route.Path)
							midware = append(midware, hdlr)
						}
					}
				}

				// check for JWT authorization
				if req.JWT != nil {
					log.Printf("[http] %s JWT filter middleware added ...", route.Path)
					midware = append(midware, checkRequestJWT(req, ro.NotFoundHandler()))
				}

				// check for POST values
				if method == http.MethodPost {
					log.Printf("[http] %s POST filter middleware added ...", route.Path)
					midware = append(midware, checkRequestPost(req, ro.NotFoundHandler()))
				}

				// check for header values
				if req.Headers != nil {
					log.Printf("[http] %s header filter middleware added ...", route.Path)
					midware = append(midware, checkRequestHeader(req, ro.NotFoundHandler()))
				}

				// add any plugin post middleware
				for k, plugin := range plugins {
					if plug, ok := plugin.(PostPluginHTTP); ok {
						requHTTP := requ.HTTP{Method: req.Method, Ticker: req.Ticker, Order: req.Order, Delay: req.Delay}
						if hdlr, ok := plug.PostMiddlewareHTTP(route.Path, req.Plugins, requHTTP); ok {
							log.Printf("[http][%s][post] %s middleware added ...", k, route.Path)
							midware = append(midware, hdlr)
						}
					}
				}

				// add cors middleware if this handler requests it
				if corsMidware != nil {
					log.Printf("[http] CORS %s added ...", route.Path)
					midware = append(midware, corsMidware)
				}

				if route.Proxy != nil {
					pxy := route.Proxy // capture for the closure...
					log.Printf("[http] proxy for %s added ...", route.Path)
					midware = append(midware, func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if proxy, ok := r.Context().Value(ctxKey(pxy.Name)).(*configProxy); ok {
								useProxy(w, r, proxy, pxy.Headers) // async call
								return
							}
						})
					})
				}

				multiResponse[method].hfs[i] = httpHandler(req, config.Texts)
				multiResponse[method].mws[i] = midware
			}
		}

		// collect all responses ..
		for method, v := range multiResponse {
			hf, mw := v.hfs[0], v.mws[0]
			v.hfs, v.mws = v.hfs[1:], v.mws[1:]

			// add the handler with the proper middleware
			log.Printf("[http] %s %s added ...", method, route.Path)
			ro.With(checkRetries(v)).With(mw...).Method(method, route.Path, hf)
		}
	}

	// check for custom not found handler
	if config.NotFound != nil {
		ro.NotFound(func(w http.ResponseWriter, r *http.Request) {
			var status = config.NotFound.Response.Status
			n, err := strconv.ParseInt(status, 10, 16)
			log.OnErr(err).Println("[error] not found parse int: %v", err)

			w.WriteHeader(int(n))
			body, _ := config.NotFound.Response.Body.Expr.Value(&bodyEvalCtx)
			fmt.Fprintln(w, body.AsString())
		})
	}

	// check for custom method not allowed handler
	if config.MethodNotAllowed != nil {
		ro.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			var status = config.MethodNotAllowed.Response.Status
			n, err := strconv.ParseInt(status, 10, 16)
			log.OnErr(err).Println("[error] method not allowed parse int: %v", err)

			w.WriteHeader(int(n))
			body, _ := config.MethodNotAllowed.Response.Body.Expr.Value(&bodyEvalCtx)
			fmt.Fprintln(w, body.AsString())
		})
	}

	re := reloadError{os: config.internal.os} // setup error handling on reload

	// check to see if we should send back headers
	// saying that the reload failed
	if !config.internal.svrCfgLoadValid {
		mw.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				scheme := "http://"
				if r.TLS != nil {
					scheme = "https://"
				}
				re.headers(config, w.Header().Add, scheme+r.Host)
				next.ServeHTTP(w, r)
			})
		})
	}

	// show errors and stats
	ro.Get("/_internal/reload/errors", re.handler(config))
	ro.Get("/_internal/server/stats", serverStats())

	// channels used for stopping all of the running servers
	var stoppers = make([]chan struct{}, len(config.Servers))
	for i := range stoppers {
		stoppers[i] = make(chan struct{})
	}

	// how we can wait until all of the servers have gracefully shutdown
	var svr = new(sync.WaitGroup)
	svr.Add(len(config.Servers))

	for i, server := range config.Servers {
		r := chi.NewRouter() // a place where we can combine middleware and routes

		tlsConfig := useTLS(r, server) // Getting our TLS status for each server

		// check if we should limit this server to only HTTP2 requests
		if server.HTTP2 {
			log.Printf("[http2] %q is restricted to only HTTP/2 requests ...", server.Name)
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, ok := w.(http.Pusher); !ok {
						http.Error(w, http.StatusText(http.StatusUpgradeRequired), http.StatusUpgradeRequired)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
		}

		if server.BasicAuth != nil {
			log.Printf("[basicAuth] %q middleware added ...", server.Name)
			r.Use(checkBasicAuth(server, ro.NotFoundHandler()))
		}

		if server.JWT != nil {
			log.Printf("[jwt] %q middleware added ...", server.Name)
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := context.WithValue(r.Context(), ctxKey(server.JWT.Name), server.JWT)
					ctx = context.WithValue(ctx, CtxKeySignature, useJWT(server))
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			})
		}

		// add server proxy configs
		if server.Proxy != nil {
			log.Printf("[proxy] %q add proxy %q lookup ...", server.Name, server.Proxy.Name)
			urlParsed, err := url.Parse(server.Proxy.URL)
			if err != nil {
				log.Fatalf("[server] %q parse proxy block: %v", server.Proxy.Name, err)
			}
			server.Proxy._url = urlParsed
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := context.WithValue(r.Context(), ctxKey(server.Proxy.Name), server.Proxy)
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			})
		}

		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), CtxKeyServerName, server.Name)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		r.Use(mw.Middlewares()...)
		r.Mount("/", ro)
		serve := &http.Server{
			Addr:      server.Host,
			Handler:   r,
			TLSConfig: tlsConfig,
		}

		// handle graceful shutdown for all started servers
		go func() {
			<-stoppers[i]
			defer svr.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := serve.Shutdown(ctx)
			log.OnErr(err).Printf("[server] graceful shutdown err: %v", err)

		}()

		// starting the server
		go func(name string) {
			if tlsConfig == nil {
				log.Printf("[server] %q starting HTTP (addr: %s) ...", name, serve.Addr)
				if err := serve.ListenAndServe(); err != http.ErrServerClosed {
					log.Fatalf("[server] HTTP ListenAndServe: %v", err)
				}
			} else {
				log.Printf("[server] %q starting HTTPS (addr: %s) ...", name, serve.Addr)
				if err := serve.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
					log.Fatalf("[server] HTTPS ListenAndServe: %v", err)
				}
			}
		}(server.Name)
	}

	shutdown := make(chan struct{}, 1)
	go func() {
		<-config.shutdown
		for _, ch := range stoppers {
			close(ch)
		}
		svr.Wait()
		close(shutdown)
	}()
	return shutdown
}

// serverStats returns the stats around each request
// NOT YET IMPLEMENTED...
func serverStats() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "addr:", r.Host)
	}
}
