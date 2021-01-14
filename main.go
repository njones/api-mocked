package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/hashicorp/hcl/v2/hclsimple"
	"github.com/njones/logger"
)

var _cfgFileLoadPath string
var log = logger.New(logger.WithTimeFormat("2006/01/02 15:04:05 -"))

func main() {
	var configFile, logDir string

	flag.StringVar(&configFile, "config", "cfg/config.hcl", "the path to the config file")
	flag.StringVar(&logDir, "log-dir", "log", "the path to the log directory")

	flag.Parse()

	flp, err := filepath.Abs(configFile)
	if err != nil {
		panic(fmt.Errorf("[server] file load: %v", err))
	}

	// all config files will be at this path
	_cfgFileLoadPath = filepath.Dir(flp)

	log.Println(run(configFile, logDir))
}

type RunOptions func(*Config)

func run(configFile string, logDir string, opts ...RunOptions) string {
	var config Config

	config.internal.file = configFile
	config.internal.svrStart = time.Now()
	config.internal.svrCfgLoadValid = true // this is only false if the reload fails...
	config.System = &system{
		LogDir: &logDir,
	}

	defer func() {
		if r := recover(); r != nil {
			saveReloadError(config, fmt.Errorf("%s", string(debug.Stack())))
		}
	}()

	log.Println("[server] applying startup options ...")
	for _, opt := range opts {
		opt(&config)
	}

	config.reload = _reload(config)
	config.shutdown = _shutdown(config)

	cs := &cleanReloadSlices{}
	for {
		// reset all of these slices because the decode will
		// have problems if on a reload they are already
		// filled in and not the same size
		config.Servers, config.Routes, config.Websockets = cs.nil() // send back nil, so these are clean to decode into

		log.Printf("[server] loading the config file: %s ...", config.internal.file)
		if err := hclsimple.DecodeFile(config.internal.file, _context(), &config); err != nil {
			if !cs.isReload() {
				log.Fatalf("cannot start server(s): %v", err)
			}
			saveReloadError(config, err)
			config.internal.svrCfgLoadValid = false
			config.Servers, config.Routes, config.Websockets = cs.get() // add the old copy back
		}
		cs.del() // remove old copy

		// run all of the servers (usually HTTP(s))
		shutdown := _http(&config)

		select {
		case <-config.reload:
			config.shutdown <- struct{}{}
			config.reloadDrain(shutdown)
			config.internal.svrCfgLoad = time.Now()
			config.internal.svrCfgLoadValid = true
			log.Println("[server] reloading ...")

			cs.put(config.Servers, config.Routes, config.Websockets) // save a copy
		case <-shutdown:
			return "Done"
		}
	}
}
