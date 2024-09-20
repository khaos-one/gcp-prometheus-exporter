package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"os"
	pgsql_prometheus_collector "pgsql-prometheus-exporter/pkg/pgsql-prometheus-collector"
	"time"
)

func main() {
	c := os.Getenv("POSTGRESQL_URL_TEMPLATE")
	metrics := pgsql_prometheus_collector.NewDatabasesCollector(c, 10*time.Minute)
	defer metrics.Close()

	r := prometheus.NewRegistry()
	r.MustRegister(metrics)

	handler := promhttp.HandlerFor(r, promhttp.HandlerOpts{})
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	err := http.ListenAndServe(":9042", mux)
	if err != nil {
		panic(err)
	}
}
