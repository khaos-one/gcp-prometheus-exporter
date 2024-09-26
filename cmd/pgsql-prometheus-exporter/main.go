package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	pgsqlprometheuscollector "pgsql-prometheus-exporter/pkg/pgsql-prometheus-collector"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	c := os.Getenv("POSTGRESQL_URL_TEMPLATE")
	if c == "" {
		log.Fatal("POSTGRESQL_URL_TEMPLATE environment variable is not set")
	}
	metrics := pgsqlprometheuscollector.NewDatabasesCollector(c, 10*time.Minute)
	defer metrics.Close()

	r := prometheus.NewRegistry()
	r.MustRegister(metrics)

	handler := promhttp.HandlerFor(r, promhttp.HandlerOpts{})
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)

	srv := &http.Server{
		Addr:    ":9042",
		Handler: mux,
	}

	// Graceful shutdown
	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)

	go func() {
		<-quit
		log.Println("Server is shutting down...")

		// Create a context with a timeout for the shutdown process
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Attempt graceful shutdown
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("Server Shutdown Failed: %+v", err)
		}

		close(done)
	}()

	log.Println("Starting server on :9042")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start server: %v", err)
	}

	<-done
	log.Println("Server stopped")
}
