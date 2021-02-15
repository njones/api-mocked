package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/afero"
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
type reloadSliceManager struct {
	s []ConfigHTTP
	r []Route
}

func (rs *reloadSliceManager) isReload() bool {
	return len(rs.s) > 0
}

func (rs *reloadSliceManager) put(s []ConfigHTTP, r []Route) {
	rs.s, rs.r = s, r
}

func (rs *reloadSliceManager) get() ([]ConfigHTTP, []Route) {
	return rs.s, rs.r
}

func (rs *reloadSliceManager) del() {
	rs.s, rs.r = rs.s[:0], rs.r[:0]
}

func (rs *reloadSliceManager) nil() ([]ConfigHTTP, []Route) {
	return []ConfigHTTP(nil), []Route(nil)
}

type reloadError struct {
	os afero.Fs
}

// reloadErrorSave make sure this only fires once for a specific error
func (re reloadError) save(config Config, save error, kind string) {
	if config.System == nil || config.System.LogDir == nil {
		return // skip logging...
	}

	f, err := re.os.Create(filepath.Join(*config.System.LogDir, fmt.Sprintf("%d-%s.txt", time.Now().Unix(), kind)))
	if err != nil {
		log.Fatal(fmt.Errorf("cannot open file to save error: %v", err))
	}
	defer f.Close()
	fmt.Fprintf(f, reloadErrorSaveOut, time.Now().Format(time.RFC1123Z), kind, save)
}

func (re reloadError) zeroOrGreater(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

func (re reloadError) hln(delim string, length int, format string, v ...interface{}) string {
	txt := fmt.Sprintf(format, v...)

	return fmt.Sprintf("%s%[1]s %s %s %[1]s%[1]s", delim, txt, strings.Repeat(" ", re.zeroOrGreater(length-len(txt)-7)))
}

// ww does word wrapping, and sends back sentences in slices
func (re reloadError) ww(txt string, length int) (rtn []string) {
	runes := []rune(strings.TrimSpace(txt)) // the code chants: "we want runes, we want runes, we want..."
	if len(runes) <= length {
		return []string{txt}
	}

	// super naive word wrapping
	// jump to where you want, and walk back
	// to a space or newline, then jump again…
	// rinse and repeat.
	for i := length; i >= 0; i-- {
		switch runes[i] {
		case '\n', ' ':
			rtn = append(rtn, string(runes[:i]))
			runes = runes[i+1:] // remove the space or newline
			i = length          // reset the length
			if len(runes) < i { // and check again
				return append(rtn, string(runes))
			}
		}
	}
	return append(rtn, string(runes))
}

func (re reloadError) headers(config *Config, fn func(string, string), hostname string) {
	var delim, bar, x = "-", "=", 60
	fn("x-reload-error", strings.Repeat(delim, x))
	fn("x-reload-error", re.hln(delim, x, "[server] started on: %s", config.internal.svrStart.Format(time.RFC1123)))
	fn("x-reload-error", re.hln(delim, x, "[server] reloaded on: %s", config.internal.svrCfgLoad.Format(time.RFC1123)))
	fn("x-reload-error", re.hln(delim, x, "[server] uptime: %s", time.Since(config.internal.svrStart)))
	fn("x-reload-error", re.hln(delim, x, strings.Repeat(bar, x-7)))
	lines := re.ww("The server configuration has not been applied after the most recent update due to an error, please check the configuration and try the reload again.", x-7)
	for _, line := range lines {
		fn("x-reload-error", re.hln(delim, x, line))
	}
	fn("x-reload-error", strings.Repeat(delim, x))
	fn("x-reload-error", fmt.Sprintf("for errors see: %s/_internal/reload/errors", hostname))
}

func (re reloadError) handler(config *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if config.System == nil || config.System.LogDir == nil {
			fmt.Fprintf(w, "no log directory set")
			return
		}

		// TODO(njones): sanitize the logDir path
		var files []string
		afero.Walk(re.os, *config.System.LogDir, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, "-reload.txt") || strings.HasSuffix(path, "-panix.txt") {
				files = append(files, path) // just save the file path, because we're gonna sort them...
			}
			return nil
		})

		// reverse this so we have the latest error file first...
		sort.Sort(sort.Reverse(sort.StringSlice(files)))

		for _, file := range files {
			f, err := re.os.Open(file)
			if err != nil {
				fmt.Fprintf(w, "error walking output log files: %v\n", err)
				return
			}
			defer f.Close()
			io.Copy(w, f)
			w.Write([]byte("\n"))
		}
	}
}

const reloadErrorSaveOut = `
---
datetime: %s
error: on %s
---

%s
`
