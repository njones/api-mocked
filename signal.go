package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func _signal(config *Config) chan struct{} {
	shutdown := make(chan struct{}, 1)
	go func() {
		signalChan := make(chan os.Signal, 1)

		signal.Notify(
			signalChan,
			syscall.SIGHUP,  // kill -SIGHUP XXXX
			syscall.SIGINT,  // kill -SIGINT XXXX or Ctrl+c
			syscall.SIGQUIT, // kill -SIGQUIT XXXX
		)

		<-signalChan
		log.Println("shutting down... (gracefully)")
		close(shutdown)
	}()
	return shutdown
}
