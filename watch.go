package main

import (
	"log"

	"github.com/fsnotify/fsnotify"
)

func _watch(config *Config, configFile string) chan struct{} {
	reload := make(chan struct{}, 1)
	go func() {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			log.Fatal(err)
		}
		defer watcher.Close()

		err = watcher.Add(configFile)
		if err != nil {
			log.Fatal("watch:", err)
		}

		for {
			log.Println("watch file...")
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Println("event:", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					reload <- struct{}{}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()
	return reload
}
