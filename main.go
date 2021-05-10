package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"plugin"
	"runtime/debug"
	"strings"
	"time"

	plug "plugins/config"

	"github.com/njones/logger"
	"github.com/spf13/afero"
)

// _runtimePath the name of the path that we use to base file loads on
var _runtimePath string

// log is the global logger used to log info
var log = logger.New(logger.WithTimeFormat("2006/01/02 15:04:05 -"))

// Plugin is the min interface needed to provide a plugin. As it allows
// a plugin to be setup
// type Plugin interface {
// 	Setup() error
// 	Version(int32) int32 // the version that API-Mocked supports is passed, the version the plugin supports
// 	Metadata() string    // a string with the plugin semver, author
// 	SetupRoot(hcl.Body) error
// 	SetupConfig(string, hcl.Body) error // can be called multiple times
// }

// Plugin is the min interface needed to provide a plugin. As it allows
// a plugin to be setup
type Plugin plug.Plugin

// RunOptions allows tests and alternative entry points (other than the
// main CLI entrypoint) to add configuration information at runtime
type RunOptions func(*Config)

type cfgFiles []string

func (flgs *cfgFiles) String() string {
	return "config files"
}

func (flgs *cfgFiles) Set(value string) error {
	*flgs = append(*flgs, value)
	return nil
}

var configFiles cfgFiles

// main starts everything
func main() {
	var logDir, pluginDir string

	flag.Var(&configFiles, "config", "the config files to load")
	flag.StringVar(&logDir, "log-dir", "log", "the path to the log directory")
	flag.StringVar(&pluginDir, "plugin-dir", "./plugins/obj", "the path to where .so plugins are stored")

	flag.Parse()

	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic(err)
	}
	_runtimePath = dir

	log.Println(run(configFiles, logDir, pluginDir))
}

// run reads configs and starts the process. This can be kicked off from tests
// to make the program more testable. Pulls in the config file location, the
// log directory (where error logs are stored) and the external .so plugin directory
// any other options should go through the RunOptions type
func run(configFiles []string, logDir string, pluginDir string, opts ...RunOptions) string {
	pluginDir = strings.TrimSuffix(pluginDir, "/") + "/" // always end with a "/"

	var config Config

	config.internal.os = afero.NewOsFs()
	config.internal.files = configFiles
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
	}(true)

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

		log.Printf("[server] loading the config files: %s ...", config.internal.files)
		if err := decodeFile(config.internal.files, _context(), &config); err != nil {
			if !mgr.isReload() {
				log.Fatalf("cannot start server(s): %v", err)
			}
			re.save(config, err, "reload")
			config.internal.svrCfgLoadValid = false
			config.Servers, config.Routes = mgr.get() // add the old copy back
		}
		mgr.del() // remove old copy

		// setup any external plugin
		if runtime.GOOS != "windows" { // we don't support external plugins on "windows"
			if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
				files, err := ioutil.ReadDir(pluginDir)
				if err != nil {
					log.Fatalf("cannot read plugin dir: %v", err)
				}

				for _, f := range files {
					ext, err := plugin.Open(pluginDir + f.Name())
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
			}
		}

		// setup any internal plugin
		for name, plugin := range plugins {
			log.Printf("[plugin] root init %v", name)
			if err := plugin.Setup(); err != nil {
				log.Printf("[setup] init plugin err: %v", err)
			}
			if err := plugin.SetupRoot(config.Plugins); err != nil {
				log.Printf("[setup] root plugin err: %v", err)
			}
			for _, svr := range config.Servers {
				if err := plugin.SetupConfig(svr.Name, svr.Plugins); err != nil {
					log.Printf("[setup] config plugin err: %v", err)
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
