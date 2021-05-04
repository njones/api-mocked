package main

import (
	"os"
	"os/signal"
	"syscall"
)

// _shutdown handles the Ctrl-C and other shutdown signals
func _shutdown(config Config) chan struct{} {
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
		log.Println("\n-----\nshutting down... (gracefully)\n-----")
		close(shutdown)
	}()

	return shutdown
}
