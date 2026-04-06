//go:build !cli

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"gioui.org/app"

	"stun_max/client/ui"
)

func main() {
	log.Println("[STUNMAX] main() started")
	var a *ui.App
	go func() {
		log.Println("[STUNMAX] goroutine: creating App")
		a = ui.NewApp()

		// Catch OS signals for cleanup (kill, Ctrl+C, etc.)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			if a != nil && a.Client != nil {
				a.Client.Disconnect()
			}
			os.Exit(0)
		}()

		if err := a.Run(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
