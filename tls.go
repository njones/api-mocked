package main

import (
	"bytes"
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
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/go-chi/chi"
)

var (
	DefaultHostPort  = ":9090"
	DefaultACMEEmail = "example@example.com"
)

func useTLS(mw *chi.Mux, server serverConfig) *tls.Config {
	if server.SSL == nil {
		log.Printf("[tls] %q no certs loaded (using HTTP) ...", server.Name)
		return nil
	}

	var err error
	var tlsConfig *tls.Config

	switch {
	case server.SSL.LetsEnc != nil:
		log.Printf("[tls] %q loading lets encrypt certs ...", server.Name)

		// provide an email address
		certmagic.DefaultACME.Email = DefaultACMEEmail
		if server.SSL.LetsEnc.Email != nil {
			val, err := server.SSL.LetsEnc.Email.Expr.Value(&fileEvalCtx)
			if err != nil {
				panic(fmt.Errorf("lets encrypt email: %v", err))
			}
			certmagic.DefaultACME.Email = val.AsString()
		}

		// use the staging endpoint while we're developing
		certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA

		tlsConfig, err = certmagic.TLS(server.SSL.LetsEnc.Hosts)
		if err != nil {
			panic(fmt.Errorf("lets encrypt SSL certs: %v", err))
		}

	case server.SSL.Crt == "" && server.SSL.Key == "":
		log.Printf("[tls] %q loading self-signed certs ...", server.Name)

		var pin []byte
		tlsConfig, pin, err = cert(server.SSL.CACrt, server.SSL.CAKey)
		if err != nil {
			panic(fmt.Errorf("gen SSL certs: %v", err))
		}

		// add Pinning Key to output ...
		mw.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Pinned-Key", fmt.Sprintf("sha256//%s", string(pin)))
				next.ServeHTTP(w, r)
			})
		})

	default:
		log.Printf("[tls] %q loading external SSL certs ...", server.Name)
		cer, err := tls.LoadX509KeyPair(server.SSL.Crt, server.SSL.Key)
		if err != nil {
			panic(fmt.Errorf("load SSL certs: %v", err)) // will stop the startup sequence...
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cer}}
	}

	return tlsConfig
}

func cert(caCrtFile, caKeyFile string) (serverTLSConf *tls.Config, pin []byte, err error) {
	var caCrt *x509.Certificate
	var caKey crypto.PrivateKey

	rnd := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	max, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFF", 16)
	if max == nil {
		return nil, nil, ErrBigIntCreation
	}

	nets, err := net.Interfaces()
	if err != nil {
		return nil, nil, ErrGetNetInterface.F(err)
	}

	var ipAddrs []net.IP
	for _, i := range nets {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, nil, ErrGetNetAddr.F(err)
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
			return nil, nil, ErrLoadX509.F(err)
		}

		if len(caCrtTLS.Certificate) > 0 {
			caCrt, err = x509.ParseCertificate(caCrtTLS.Certificate[0])
			if err != nil {
				return nil, nil, ErrParseCACert.F(err)
			}
			caKey = caCrtTLS.PrivateKey
		}
	}

	svrCrtPrvKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return nil, nil, ErrGenKey.F(err)
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
		return nil, nil, ErrCreateX590Cert.F(err)
	}

	svrCrtPEM := new(bytes.Buffer)
	pem.Encode(svrCrtPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: svrCrtBytes,
	})

	svrCrtPrvKeyDER, err := x509.MarshalECPrivateKey(svrCrtPrvKey)
	if err != nil {
		return nil, nil, ErrMarshalPrivKey.F(err)
	}
	svrCrtPrvKeyPEM := new(bytes.Buffer)

	pem.Encode(svrCrtPrvKeyPEM, &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: svrCrtPrvKeyDER,
	})

	serverCert, err := tls.X509KeyPair(svrCrtPEM.Bytes(), svrCrtPrvKeyPEM.Bytes())
	if err != nil {
		return nil, nil, ErrCreateTLSCert.F(err)
	}

	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}

	svrCrtPubKeyDER, err := x509.MarshalPKIXPublicKey(&svrCrtPrvKey.PublicKey)
	if err != nil {
		return nil, nil, ErrMarshalPubKey.F(err)
	}

	sum := sha256.Sum256(svrCrtPubKeyDER)
	pin = make([]byte, base64.StdEncoding.EncodedLen(len(sum)))
	base64.StdEncoding.Encode(pin, sum[:])

	return serverTLSConf, pin, err
}
