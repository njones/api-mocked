package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

func _reload(config Config) chan struct{} {
	reload := make(chan struct{}, 1)

	go func() {
		watcher, err := fsnotify.NewWatcher()
		if log.OnErr(err).Printf("[server] setting up watcher: %v", err).HasErr() {
			return
		}
		defer watcher.Close()

		err = watcher.Add(config.internal.file)
		log.OnErr(err).Printf("[server] adding watcher: %v", err)

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					reload <- struct{}{}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.OnErr(err).Printf("[server] watcher error: %v", err)
			}
		}
	}()

	return reload
}

// This is for setting cleaning up and setting slices on a
// reload. The current HCL parser has touble when the slices
// are already filled in and an there are new items to be
// added (there's) a bad slice index on reflection error.
// this is just a way to manage adding and removing the slices
// and cleaning up
type cleanReloadSlices struct {
	s []serverConfig
	r []route
	w []websocket
}

func (rs *cleanReloadSlices) isReload() bool {
	return len(rs.s) > 0
}

func (rs *cleanReloadSlices) put(s []serverConfig, r []route, w []websocket) {
	rs.s, rs.r, rs.w = s, r, w
}

func (rs *cleanReloadSlices) get() ([]serverConfig, []route, []websocket) {
	return rs.s, rs.r, rs.w
}

func (rs *cleanReloadSlices) del() {
	rs.s, rs.r, rs.w = rs.s[:0], rs.r[:0], rs.w[:0]
}

func (rs *cleanReloadSlices) nil() ([]serverConfig, []route, []websocket) {
	return []serverConfig(nil), []route(nil), []websocket(nil)
}

// saveReloadError make sure this only fires once for a specific error
func saveReloadError(config Config, save error) {
	f, err := os.Create(filepath.Join(*config.System.LogDir, fmt.Sprintf("%d-reload.txt", time.Now().Unix())))
	if err != nil {
		log.Fatal(fmt.Errorf("cannot open file to save reload error: %v", err))
	}
	defer f.Close()
	fmt.Fprintln(f, save)
}
