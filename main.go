package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ts/internal/config"
	"ts/internal/server"
)

func main() {
	path := flag.String("config", "config.toml", "TOML configuration file")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	app := &http.Server{Addr: cfg.Server.Listen, Handler: server.New(cfg), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	drained := make(chan struct{})
	go func() {
		<-ctx.Done()
		if err := server.Shutdown(app); err != nil {
			log.Print(err)
		}
		close(drained)
	}()
	log.Printf("ts listening on %s", cfg.Server.Listen)
	if err := app.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
}
