package pgsql_prometheus_collector

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	"log"
	"regexp"
	"time"
)

type databaseTable struct {
	name              string
	size              int64
	sizePretty        string
	rowsCountEstimate int64

	sizeMetric              *prometheus.Desc
	rowsCountEstimateMetric *prometheus.Desc
}

func createSizeMetric(tableName, databaseName string) *prometheus.Desc {
	return prometheus.NewDesc(
		"pgsql_table_size",
		"Table size in bytes",
		nil,
		prometheus.Labels{"table": tableName, "database": databaseName},
	)
}

func createRowsCountEstimateMetric(tableName, databaseName string) *prometheus.Desc {
	return prometheus.NewDesc(
		"pgsql_table_rows_count_estimate",
		"Rows count estimate (might be not accurate)",
		nil,
		prometheus.Labels{"table": tableName, "database": databaseName},
	)
}

func newTable(name string, size int64, sizePretty string, rowsCountEstimate int64, databaseName string) *databaseTable {
	sizeMetric := createSizeMetric(name, databaseName)
	rowsCountEstimateMetric := createRowsCountEstimateMetric(name, databaseName)

	return &databaseTable{
		name:                    name,
		size:                    size,
		sizePretty:              sizePretty,
		rowsCountEstimate:       rowsCountEstimate,
		sizeMetric:              sizeMetric,
		rowsCountEstimateMetric: rowsCountEstimateMetric,
	}
}

type database struct {
	name             string
	connectionString string
	size             int64
	sizePretty       string

	sizeMetric *prometheus.Desc

	tables []*databaseTable
}

func newDatabase(connectionStringPattern *string, name string, size int64, sizePretty string) *database {
	if connectionStringPattern == nil {
		log.Fatal("connectionStringPattern cannot be nil")
	}

	c := fmt.Sprintf(*connectionStringPattern, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := pgx.Connect(ctx, c)
	if err != nil {
		log.Printf("Database connection error: %v", err)
		return nil
	}
	defer func() {
		if err := db.Close(ctx); err != nil {
			log.Printf("Error closing database connection: %v", err)
		}
	}()

	rows_handle, err := db.Query(ctx, `
        SELECT
            t.table_name AS name,
            pg_size_pretty(pg_total_relation_size(quote_ident(t.table_name))) AS size_pretty,
            pg_total_relation_size(quote_ident(t.table_name)) AS size,
            p.reltuples AS rows_estimate
        FROM information_schema.tables t
        LEFT JOIN pg_class p
            ON p.relname = t.table_name
        WHERE table_schema = 'public'
        ORDER BY 3 DESC;`)
	if err != nil {
		log.Printf("Query execution error: %v", err)
		return nil
	}
	defer rows_handle.Close()

	rows, err := pgx.CollectRows(rows_handle, pgx.RowToMap)
	if err != nil {
		log.Printf("Rows collection error: %v", err)
		return nil
	}

	tables := lo.Map(rows, func(item map[string]any, _ int) *databaseTable {
		tableName, ok := item["name"].(string)
		if !ok {
			log.Fatal("Unexpected type for table name")
		}
		size, ok := item["size"].(int64)
		if !ok {
			log.Fatal("Unexpected type for size")
		}
		sizePretty, ok := item["size_pretty"].(string)
		if !ok {
			log.Fatal("Unexpected type for sizePretty")
		}
		rowsCountEstimate, ok := item["rows_estimate"].(float32)
		if !ok {
			log.Fatal("Unexpected type for rows estimate")
		}
		return newTable(tableName, size, sizePretty, int64(rowsCountEstimate), name)
	})

	sizeMetric := prometheus.NewDesc(
		"pgsql_database_size",
		"Database size (in bytes)",
		nil,
		prometheus.Labels{
			"database": name,
		},
	)

	return &database{
		name:             name,
		connectionString: c,
		size:             size,
		sizePretty:       sizePretty,
		tables:           tables,
		sizeMetric:       sizeMetric,
	}
}

type DatabasesCollector struct {
	connectionStringPattern string
	databases               []*database
	ticker                  *time.Ticker
	tickerDone              chan bool
}

var (
	validPostgresURL = regexp.MustCompile(`^postgres://[^:]+:[^@]+@[^\s@]+:\d+/%s$`)
)

func isValidPostgresURL(input string) bool {
	return validPostgresURL.MatchString(input)
}

func NewDatabasesCollector(connectionStringPattern string, updateInterval time.Duration) *DatabasesCollector {
	if !isValidPostgresURL(connectionStringPattern) {
		log.Fatal("Invalid connection string pattern")
	}

	// Default update interval
	if updateInterval == 0 {
		updateInterval = 15 * time.Minute
	}

	// Initialize DatabasesCollector
	collector := &DatabasesCollector{
		connectionStringPattern: connectionStringPattern,
		databases:               []*database{},
		ticker:                  time.NewTicker(updateInterval),
		tickerDone:              make(chan bool),
	}

	// Initial update
	err := collector.update()
	if err != nil {
		log.Printf("Initial update failed: %v", err)
	}

	// Goroutine for periodic updates
	go func() {
		for {
			select {
			case <-collector.tickerDone:
				return
			case <-collector.ticker.C:
				if err := collector.update(); err != nil {
					log.Printf("Update failed: %v", err)
				}
			}
		}
	}()

	return collector
}

func (d *DatabasesCollector) Close() {
	d.ticker.Stop()
	d.tickerDone <- true
}

func (d *DatabasesCollector) update() error {
	log.Println("Database statistics update started")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	c := fmt.Sprintf(d.connectionStringPattern, "postgres")
	db, err := pgx.Connect(ctx, c)
	if err != nil {
		log.Printf("Failed to connect to database: %v", err)
		return err
	}
	defer db.Close(ctx)

	rows_handle, err := db.Query(
		ctx,
		"select datname from pg_database where datistemplate = false;")
	if err != nil {
		log.Printf("Failed to fetch databases: %v", err)
		return err
	}
	defer rows_handle.Close()

	rows, err := pgx.CollectRows(rows_handle, pgx.RowToMap)
	if err != nil {
		log.Printf("Failed to collect rows: %v", err)
		return err
	}

	databases := lo.Map(
		lo.Filter(rows, func(item map[string]interface{}, _ int) bool {
			datname, ok := item["datname"].(string)
			if !ok {
				log.Printf("Unexpected type for datname")
				return false
			}
			return datname != "cloudsqladmin"
		}),
		func(item map[string]interface{}, _ int) *database {
			name, ok := item["datname"].(string)
			if !ok {
				log.Printf("Unexpected type for datname")
				return nil
			}
			var size int64
			var sizePretty string
			err := db.QueryRow(
				ctx,
				"SELECT pg_database_size($1), pg_size_pretty(pg_database_size($1))",
				name).Scan(&size, &sizePretty)
			if err != nil {
				log.Printf("Query for database size failed: %v", err)
				return nil
			}
			return newDatabase(&d.connectionStringPattern, name, size, sizePretty)
		})

	d.databases = databases
	log.Println("Database statistics update finished")

	return nil
}

func (dc *DatabasesCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, db := range dc.databases {
		if db.sizeMetric != nil {
			ch <- db.sizeMetric
		}
		for _, table := range db.tables {
			if table.sizeMetric != nil {
				ch <- table.sizeMetric
			}
			if table.rowsCountEstimateMetric != nil {
				ch <- table.rowsCountEstimateMetric
			}
		}
	}
}

func (d *DatabasesCollector) Collect(ch chan<- prometheus.Metric) {
	for _, d := range d.databases {
		m := prometheus.MustNewConstMetric(d.sizeMetric, prometheus.GaugeValue, float64(d.size/1024/1024))
		m = prometheus.NewMetricWithTimestamp(time.Now(), m)
		ch <- m
		for _, table := range d.tables {
			m := prometheus.MustNewConstMetric(table.sizeMetric, prometheus.GaugeValue, float64(table.size/1024/1024))
			m = prometheus.NewMetricWithTimestamp(time.Now(), m)
			ch <- m

			m = prometheus.MustNewConstMetric(table.rowsCountEstimateMetric, prometheus.GaugeValue, float64(table.rowsCountEstimate))
			m = prometheus.NewMetricWithTimestamp(time.Now(), m)
			ch <- m
		}
	}
}
