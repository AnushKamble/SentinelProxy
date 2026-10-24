package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

func main() {
	configPath := flag.String("config", "configs/routes.example.yaml", "Path to SentinelProxy YAML configuration file")
	flag.Parse()

	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration from %s: %v", *configPath, err)
	}

	application, err := app.New(cfg, *configPath)
	if err != nil {
		log.Fatalf("Failed to initialize SentinelProxy: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		if err := application.Start(); err != nil {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		log.Fatalf("Server startup failed: %v", err)
	case sig := <-sigChan:
		fmt.Printf("\nReceived signal %s, initiating graceful shutdown...\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()

		if err := application.Shutdown(ctx); err != nil {
			log.Fatalf("Graceful shutdown failed: %v", err)
		}
		fmt.Println("SentinelProxy shutdown complete.")
	}
}
