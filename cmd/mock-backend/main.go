package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func main() {
	port := flag.Int("port", 9001, "Port to listen on")
	id := flag.String("id", "backend-1", "Backend identifier")
	flag.Parse()

	server := mock.NewServer(*id)
	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		log.Printf("[%s] Shutting down mock backend on %s...", *id, addr)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("[%s] Error during shutdown: %v", *id, err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("[%s] Mock backend listening on http://%s", *id, addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[%s] ListenAndServe failed: %v", *id, err)
	}

	<-idleConnsClosed
	log.Printf("[%s] Mock backend exited cleanly.", *id)
}
