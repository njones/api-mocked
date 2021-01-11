package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/rs/xid"
)

var (
	DefaultHostPort  = ":9090"
	DefaultACMEEmail = "example@example.com"
)

type ctxKey string

var sigCtxKey = ctxKey("_sig_")

func _serve(config *Config) chan struct{} {
	shutdown := make(chan struct{}, 1)
	go func() {

		clientPN := newPubNubClient()
		for _, cfg := range config.PubNubConfig {
			if cfg.UUID == "" {
				cfg.UUID = xid.New().String()
			}

			publishKey, err := cfg.PublishKey.Expr.Value(&pnContext)
			if err != nil {
				panic(err)
			}

			subscribeKey, err := cfg.SubscribeKey.Expr.Value(&pnContext)
			if err != nil {
				panic(err)
			}

			clientPN.Add(
				cfg.Name,
				cfg.Channel,
				publishKey.AsString(),
				subscribeKey.AsString(),
				cfg.UUID,
			)
		}

		config.done.loadPubNubConfig <- clientPN

		r := chi.NewRouter()

		for _, v := range config.Routes {
			if v.Request == nil && v.SocketIO == nil {
				log.Fatal("need to have a response or socketio")
			}

			if v.CORS != nil {
				r.Method("OPTIONS", v.Path, corsHandler(v.CORS))
			}

			var mwHeaderHandler = make(map[string]chi.Middlewares)
			if v.Request != nil {
				for _, request := range v.Request {
					methods := strings.Split(request.Method, "|")
					for _, method := range methods {
						fmt.Printf("%s: '%s'\n%s\n", method, v.Path, v.Desc)
						method = strings.TrimSpace(method)

						keys, varKeys := request.Headers.Keys()
						if len(keys) > 0 && len(keys) != varKeys {
							mwHeaderHandler[method] = append(mwHeaderHandler[method], reqMiddleware(request, v.CORS))
							continue
						}

						if len(request.Response) > 0 {
							r.Method(method, v.Path, reqHandler(request, v.CORS))
						}
						if len(request.SocketIO) > 0 {
							if r.Match(chi.NewRouteContext(), strings.ToUpper(method), v.Path) {
								var prev http.Handler
								for _, rv := range r.Routes() {
									if rv.Pattern == v.Path {
										prev = rv.Handlers[strings.ToUpper(method)]
										break
									}
								}
								r.Method(method, v.Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
									wsHandler(request).ServeHTTP(w, r)
									prev.ServeHTTP(w, r)
								}))
							} else {
								r.Method(method, v.Path, wsHandler(request))
							}
						}
						if len(request.PubNub) > 0 {
							if r.Match(chi.NewRouteContext(), strings.ToUpper(method), v.Path) {
								var prev http.Handler
								for _, rv := range r.Routes() {
									if rv.Pattern == v.Path {
										prev = rv.Handlers[strings.ToUpper(method)]
										break
									}
								}
								r.Method(method, v.Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
									pnHandler(request, clientPN).ServeHTTP(w, r)
									prev.ServeHTTP(w, r)
								}))
							} else {
								r.Method(method, v.Path, pnHandler(request, clientPN))
							}
						}
					}
				}
			}

			for method, middlewares := range mwHeaderHandler {
				r.With(middlewares...).Method(method, v.Path, http.HandlerFunc(func(_w http.ResponseWriter, _r *http.Request) {
					r.NotFoundHandler().ServeHTTP(_w, _r)
				}))
			}

			if v.SocketIO != nil {
				r.Handle(v.Path, _socketio(v.SocketIO))
			}
		}

		if config.NotFound != nil {
			r.NotFound(func(w http.ResponseWriter, r *http.Request) {
				var status = config.NotFound.Response.Status
				n, err := strconv.ParseInt(status, 10, 16)
				if err != nil {
					log.Fatal(err)
				}
				w.WriteHeader(int(n))
				body, _ := config.NotFound.Response.Body.Expr.Value(nil)
				fmt.Fprintln(w, body.AsString())
			})
		}

		if config.MethodNotAllowed != nil {
			r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
				var status = config.MethodNotAllowed.Response.Status
				n, err := strconv.ParseInt(status, 10, 16)
				if err != nil {
					log.Fatal(err)
				}
				w.WriteHeader(int(n))
				fmt.Fprintln(w, config.MethodNotAllowed.Response.Body)
			})
		}

		var dir string
		if config.System != nil && config.System.ErrorsDir != nil {
			dir = *config.System.ErrorsDir
		}
		r.Get("/_internal/issues", func(w http.ResponseWriter, r *http.Request) { io.Copy(w, showError(dir)) })

		if config.Server == nil {
			config.Server = append(config.Server, serverConfig{})
		}

		var err error
		var svr = new(sync.WaitGroup)
		var stops []chan struct{}

		for _, server := range config.Server {
			var mid chi.Middlewares

			router := chi.NewRouter()

			stop := make(chan struct{})
			stops = append(stops, stop)

			if server.Host == "" {
				server.Host = DefaultHostPort
			}

			if server.BasicAuth != nil {
				username := server.BasicAuth.User
				password := server.BasicAuth.Pass

				mid = append(mid, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if user, pass, ok := r.BasicAuth(); ok {
							if username == user && password == pass {
								next.ServeHTTP(w, r)
								return
							}
						}
						http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					})
				})
			}

			var tlsConfig *tls.Config
			if server.SSL != nil {
				switch {
				case len(server.SSL.LetsEnc) != 0:
					log.Println("[tls] loading lets encrypt certs...")

					// provide an email address
					certmagic.DefaultACME.Email = DefaultACMEEmail

					// use the staging endpoint while we're developing
					certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA

					tlsConfig, err = certmagic.TLS(server.SSL.LetsEnc)
					if err != nil {
						log.Fatal("lets encrypt SSL certs:", err)
					}

				case server.SSL.Crt == "" && server.SSL.Key == "":
					var pin []byte
					log.Println("[tls] loading self-signed certs...")
					tlsConfig, pin, err = cert(server.SSL.CACrt, server.SSL.CAKey)
					if err != nil {
						log.Fatal("gen SSL certs:", err)
					}

					mid = append(mid, func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.Header().Set("X-Pinned-Key", fmt.Sprintf("sha256//%s", string(pin)))
							next.ServeHTTP(w, r)
						})
					})

				default:
					log.Println("[tls] loading external SSL certs...")
					cer, err := tls.LoadX509KeyPair(server.SSL.Crt, server.SSL.Key)
					if err != nil {
						log.Fatal("load SSL certs:", err)
					}
					tlsConfig = &tls.Config{Certificates: []tls.Certificate{cer}}
				}
			}

			if server.JWT != nil {
				var sigKey interface{}
				switch strings.ToLower(server.JWT.Alg)[:2] {
				case "hs":
					sigKey = []byte(server.JWT.Secret)
				case "rs":
					if val, dia := server.JWT.Key.Expr.Value(&bodyContext); !dia.HasErrors() {
						signKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(val.AsString()))
						if err != nil {
						}
						sigKey = signKey
					}
				case "es":
				case "ps":
				case "ed":
				}

				mid = append(mid, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ctx := context.WithValue(r.Context(), ctxKey(server.JWT.Name), server.JWT)
						ctx = context.WithValue(ctx, sigCtxKey, sigKey)
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				})

			}

			if server.HTTP2 {
				mid = append(mid, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if _, ok := w.(http.Pusher); !ok {
							http.Error(w, http.StatusText(http.StatusUpgradeRequired), http.StatusUpgradeRequired)
							return
						}
						next.ServeHTTP(w, r)
					})
				})

			}

			router.Use(mid...)
			router.Mount("/", r)
			serve := &http.Server{
				Addr:      server.Host,
				Handler:   router,
				TLSConfig: tlsConfig,
			}

			svr.Add(1)
			go func() {
				<-stop
				defer svr.Done()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := serve.Shutdown(ctx); err != nil {
					log.Println("shutdown err:", err)
				}
			}()

			go func() {
				if tlsConfig == nil {
					log.Printf("starting HTTP [%s] server...", serve.Addr)
					if err := serve.ListenAndServe(); err != http.ErrServerClosed {
						log.Fatalf("HTTP server ListenAndServe: %v", err)
					}
				} else {
					log.Printf("starting HTTPS [%s] server...", serve.Addr)
					if err := serve.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
						log.Fatalf("HTTPS server ListenAndServe: %v", err)
					}
				}
			}()
		}
		go func() {
			<-config.shutdown
			for _, ch := range stops {
				close(ch)
			}
		}()
		svr.Wait()
		close(shutdown)
	}()

	return shutdown
}

func cert(caCrtFile, caKeyFile string) (serverTLSConf *tls.Config, pin []byte, err error) {
	var caCrt *x509.Certificate
	var caKey crypto.PrivateKey

	rnd := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	max, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFF", 16)
	if max == nil {
		return nil, nil, fmt.Errorf("failed creating a big number")
	}

	nets, err := net.Interfaces()
	if err != nil {
		return nil, nil, fmt.Errorf("failed aquiring a network interface: %v", err)
	}

	var ipAddrs []net.IP
	for _, i := range nets {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, nil, fmt.Errorf("failed network address: %v", err)
		}
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				ipAddrs = append(ipAddrs, v.IP)
			case *net.IPAddr:
				ipAddrs = append(ipAddrs, v.IP)
			}
		}
	}

	if caCrtFile != "" {
		caCrtTLS, err := tls.LoadX509KeyPair(caCrtFile, caKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("failed loading ca: %v", err)
		}

		if len(caCrtTLS.Certificate) > 0 {
			caCrt, err = x509.ParseCertificate(caCrtTLS.Certificate[0])
			if err != nil {
				return nil, nil, fmt.Errorf("failed parsing the ca cert: %v", err)
			}
			caKey = caCrtTLS.PrivateKey
		}
	}

	svrCrtPrvKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed generating key: %v", err)
	}

	svrCrt := x509.Certificate{
		SerialNumber: new(big.Int).Rand(rnd, max),
		Subject: pkix.Name{
			Organization: []string{"Not a Organization Inc."},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour * 24 * 180),
		DNSNames:              []string{"localhost"},
		IPAddresses:           ipAddrs,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	if caCrt == nil {
		caCrt = &svrCrt
		caKey = svrCrtPrvKey
	}

	svrCrtBytes, err := x509.CreateCertificate(rand.Reader, &svrCrt, caCrt, &svrCrtPrvKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed creating a x509 certificate: %v", err)
	}

	svrCrtPEM := new(bytes.Buffer)
	pem.Encode(svrCrtPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: svrCrtBytes,
	})

	svrCrtPrvKeyDER, err := x509.MarshalECPrivateKey(svrCrtPrvKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed marshaling private key: %v", err)
	}
	svrCrtPrvKeyPEM := new(bytes.Buffer)

	pem.Encode(svrCrtPrvKeyPEM, &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: svrCrtPrvKeyDER,
	})

	serverCert, err := tls.X509KeyPair(svrCrtPEM.Bytes(), svrCrtPrvKeyPEM.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("failed creating tls cert: %v", err)
	}

	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}

	svrCrtPubKeyDER, err := x509.MarshalPKIXPublicKey(&svrCrtPrvKey.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed marshaling public key: %v", err)
	}

	sum := sha256.Sum256(svrCrtPubKeyDER)
	pin = make([]byte, base64.StdEncoding.EncodedLen(len(sum)))
	base64.StdEncoding.Encode(pin, sum[:])

	return serverTLSConf, pin, err
}
