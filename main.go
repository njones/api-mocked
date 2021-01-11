package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsimple"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

var reloadErrors []string

// TODO(njones): Allow a request to accept a JWT and access parts of it
// TODO(njones): Convert from Markdown to HCL and run

func contxt() *hcl.EvalContext {
	var paramImpl = func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		str := args[0].AsString()
		if len(args) != 2 {
			str = strings.ToLower(str)
		}
		return cty.StringVal(fmt.Sprintf("{{ .%s }}", strings.Title(str))), nil
	}

	var ctx = &hcl.EvalContext{
		Functions: map[string]function.Function{
			"env": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "var",
						Type: cty.String,
					},
				},
				Type: function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
					return cty.StringVal(os.Getenv(args[0].AsString())), nil
				},
			}),
			"param": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "key",
						Type: cty.String,
					},
				},
				VarParam: &function.Parameter{
					Name: "raw",
					Type: cty.Bool,
				},
				Type: function.StaticReturnType(cty.String),
				Impl: paramImpl,
			}),
			"raw": function.New(&function.Spec{
				Params: []function.Parameter{
					{
						Name: "key",
						Type: cty.String,
					},
				},
				Type: function.StaticReturnType(cty.String),
				Impl: paramImpl,
			}),
		},
	}

	return ctx
}

var _cfgFileLoadPath string

func main() {
	var configFile string

	flag.StringVar(&configFile, "cfg-file", "cfg/config.hcl", "the path to the config file")
	flag.Parse()

	flp, err := filepath.Abs(configFile)
	if err != nil {
		log.Fatalf("file load: %v", err)
	}

	// all config files will be at this path
	_cfgFileLoadPath = filepath.Dir(flp)

	done := Main(configFile)
	fmt.Println(done)
}

type MainOptions func(*Config)

func Main(configFile string, opts ...MainOptions) string {
	var config Config
	var runningConfig string

	config.internal.serverStart = time.Now()
	config.reload = _watch(&config, configFile)
	config.shutdown = _signal(&config)
	config.done = loading{
		loadPubNubConfig: make(chan *pnClient),
	}

	for _, opt := range opts {
		opt(&config)
	}

LoadConfig:
	var dir string
	if config.System != nil && config.System.ErrorsDir != nil {
		dir = *config.System.ErrorsDir
	} else if config.System == nil {
		a, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		config.System = &struct {
			RunName   *string "hcl:\"run_name\""
			ErrorsDir *string "hcl:\"dir\""
		}{
			ErrorsDir: &a,
		}
	}

	runningConfig = filepath.Join(dir, "api-mawk.running")
	decodeError := hclsimple.DecodeFile(configFile, contxt(), &config)
	if decodeError != nil {
		saveError(dir, "decode", decodeError)
		if runningConfig == "" { // this is the first attempt... so nothing to reload from.
			panic(fmt.Errorf("invalid config: %v", decodeError))
		}
		configFile = runningConfig // TODO(njones): make sure tha	t we know that we're using the old config when reporting the error
	} else {
		config.internal.serverConfigLoad = time.Now()
		src, err := os.Open(configFile)
		if err != nil {
			saveError(dir, "open", err) // TODO(njones): make sure that we know that we're using the old config when reporting the error
			configFile = runningConfig
			goto LoadConfig // restart with the previous running config
		}
		dst, err := os.Create(runningConfig)
		if err != nil {
			saveError(dir, "create", err)
			configFile = runningConfig
			goto LoadConfig // restart with the previous running config
		}
		_, err = io.Copy(dst, src)
		if err != nil {
			saveError(dir, "copy", err) // assume that things are corrupted and stop
			dir, _ := os.UserHomeDir()
			if f, err := os.Open(filepath.Join(dir, "api-mawk.min.bak.server")); err == nil {
				if err := gob.NewDecoder(f).Decode(&config); err != nil {
					saveError(dir, "config-gob-decode", err)
				}
			} else {
				os.Exit(6)
			}
		} else {
			dst.Close()
			src.Close()
			// TODO(njones): write out min working config so a backup server can work...
			if dir, err := os.UserHomeDir(); err == nil {
				if backupServer, err := os.Create(filepath.Join(dir, "api-mawk.min.bak.server")); err == nil {
					if err := gob.NewEncoder(backupServer).Encode(Config{Server: config.Server}); err == nil {
						backupServer.Close()
					} else {
						saveError(dir, "config-gob-encode", err)
					}
				} else {
					saveError(dir, "backup-server", err)
				}
			} else {
				saveError(dir, "home-dir", err)
			}
		}
	}

	shutdown := _serve(&config)
	_pubnub(&config)

	select {
	case <-config.reload:
		config.shutdown <- struct{}{}
		go func() {
			for {
				select {
				case <-config.reload: // there are mutiple reloads in quick sucession that need to be captured
				case <-shutdown:
					return
				}
			}
		}()
		<-shutdown
		goto LoadConfig
	case <-shutdown:
		return "Done"
	}
}

/*
ROAD MAP
a proxy recording and optionally modifying requests and responses

*/
