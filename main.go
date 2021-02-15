package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"plugin"
	"runtime/debug"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsimple"
	"github.com/njones/logger"
	"github.com/spf13/afero"
)

var _cfgFileLoadPath string
var log = logger.New(logger.WithTimeFormat("2006/01/02 15:04:05 -"))

type Plugin interface {
	Setup() error
	SetupRoot(hcl.Body) error
	SetupConfig(string, hcl.Body) error // can be called multiple times
}

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

	config.internal.os = afero.NewOsFs()
	config.internal.file = configFile
	config.internal.svrStart = time.Now()
	config.internal.svrCfgLoadValid = true // this is only false if the reload fails...
	config.System = &system{
		LogDir: &logDir,
	}

	log.Println("[server] applying startup options ...")
	for _, opt := range opts {
		opt(&config)
	}

	re := reloadError{os: config.internal.os}
	func(b bool) { // this function is so we can develop quickly...
		if b { // save any panics so we can recover from them
			defer func() {
				if r := recover(); r != nil {
					re.save(config, fmt.Errorf("%s", string(debug.Stack())), "panic")
					log.Fatal(r)
				}
			}()
		}
	}(false)

	if config.System != nil && config.System.LogDir != nil {
		if _, err := config.internal.os.Stat(*config.System.LogDir); os.IsNotExist(err) {
			log.Fatalf("[server] the log dir: %v does not exist", *config.System.LogDir)
		}
	} else {
		log.Println("[server] SKIPPING logging of reload and panic errors")
	}

	config.reload = _reload(config)
	config.shutdown = _shutdown(config)

	mgr := new(reloadSliceManager)
	for {
		// reset all of these slices because the decode will
		// have problems if on a reload they are already
		// filled in and not the same size
		config.Servers, config.Routes = mgr.nil() // send back nil, so these are clean to decode into

		log.Printf("[server] loading the config file: %s ...", config.internal.file)
		if err := hclsimple.DecodeFile(config.internal.file, _context(), &config); err != nil {
			if !mgr.isReload() {
				log.Fatalf("cannot start server(s): %v", err)
			}
			re.save(config, err, "reload")
			config.internal.svrCfgLoadValid = false
			config.Servers, config.Routes = mgr.get() // add the old copy back
		}
		mgr.del() // remove old copy

		// setup any external plugin
		files, err := ioutil.ReadDir("./plugins/obj")
		if err != nil {
			log.Fatalf("cannot read plugin dir: %v", err)
		}

		for _, f := range files {
			ext, err := plugin.Open("./plugins/obj/" + f.Name())
			if err != nil {
				log.Fatalf("cannot load external plugins: %v", err)
			}

			setup, err := ext.Lookup("SetupPluginExt")
			if err != nil {
				log.Fatalf("cannot lookup setup for plugin: %s %v", f.Name(), err)
			}

			log.Printf("[init] loading external plugin %s ...", f.Name())
			pluginName, pluginNew := setup.(func() (string, interface{}))()
			if plug, ok := pluginNew.(interface{ WithLogger(logger.Logger) }); ok {
				plug.WithLogger(log)
			}
			plugins[pluginName] = pluginNew.(Plugin)
		}

		// setup any internal plugin
		for name, plugin := range plugins {
			log.Printf("[plugin] root init %v", name)
			if err := plugin.Setup(); err != nil {
				log.Println("[setup] init plugin err: %v", err)
			}
			if err := plugin.SetupRoot(config.Plugins); err != nil {
				log.Println("[setup] root plugin err: %v", err)
			}
			for _, svr := range config.Servers {
				if err := plugin.SetupConfig(svr.Name, svr.Plugins); err != nil {
					log.Println("[setup] root plugin err: %v", err)
				}
			}
		}

		// run all of the servers (usually HTTP(s))
		shutdown := _http(&config)

		select {
		case <-config.reload:
			config.shutdown <- struct{}{}
			config.reloadDrain(shutdown)
			config.internal.svrCfgLoad = time.Now()
			config.internal.svrCfgLoadValid = true
			log.Println("[server] reloading ...")

			mgr.put(config.Servers, config.Routes) // save a copy
		case <-shutdown:
			return "Done"
		}
	}
}
