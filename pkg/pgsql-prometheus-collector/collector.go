package pgsql_prometheus_collector

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	"log"
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

func newTable(name string, size int64, sizePretty string, rowsCountEstimate int64, databaseName string) *databaseTable {
	return &databaseTable{
		name:              name,
		size:              size,
		sizePretty:        sizePretty,
		rowsCountEstimate: rowsCountEstimate,
		sizeMetric: prometheus.NewDesc(
			"pgsql_table_size",
			"Table size in bytes",
			nil,
			prometheus.Labels{"table": name, "database": databaseName}),
		rowsCountEstimateMetric: prometheus.NewDesc(
			"pgsql_table_rows_count_estimate",
			"Rows count estimate (might be not accurate)",
			nil,
			prometheus.Labels{"table": name, "database": databaseName}),
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
	c := fmt.Sprintf(*connectionStringPattern, name)
	db, err := pgx.Connect(context.Background(), c)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close(context.Background())

	rows_handle, err := db.Query(
		context.Background(),
		`select
  t.table_name as name,
  pg_size_pretty(pg_total_relation_size(quote_ident(t.table_name))) as size_pretty,
  pg_total_relation_size(quote_ident(t.table_name)) as size,
  p.reltuples as rows_estimate
from information_schema.tables t
left join pg_class p on p.relname = t.table_name
where table_schema = 'public'
order by 3 desc;`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows_handle.Close()

	rows, err := pgx.CollectRows(rows_handle, pgx.RowToMap)
	if err != nil {
		log.Fatal(err)
	}

	tables := lo.Map(rows, func(item map[string]any, _ int) *databaseTable {
		tableName := item["name"].(string)
		size := item["size"].(int64)
		sizePretty := item["size_pretty"].(string)
		rowsCountEstimate := int64(item["rows_estimate"].(float32))

		return newTable(tableName, size, sizePretty, rowsCountEstimate, name)
	})

	sizeMetric := prometheus.NewDesc(
		"pgsql_database_size",
		"Database size (in bytes)",
		nil,
		prometheus.Labels{
			"database": name,
		})

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

func NewDatabasesCollector(connectionStringPattern string, updateInterval time.Duration) *DatabasesCollector {
	if updateInterval == 0 {
		updateInterval = 15 * time.Minute
	}

	r := &DatabasesCollector{
		connectionStringPattern: connectionStringPattern,
		databases:               []*database{},
		ticker:                  time.NewTicker(updateInterval),
		tickerDone:              make(chan bool),
	}
	r.update()

	go func() {
		for {
			select {
			case <-r.tickerDone:
				return
			case <-r.ticker.C:
				r.update()
			}
		}
	}()

	return r
}

func (d *DatabasesCollector) Close() {
	d.ticker.Stop()
	d.tickerDone <- true
}

func (d *DatabasesCollector) update() {
	c := fmt.Sprintf(d.connectionStringPattern, "postgres")
	db, err := pgx.Connect(context.Background(), c)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close(context.Background())

	rows_handle, err := db.Query(
		context.Background(),
		"select datname from pg_database where datistemplate = false;")
	if err != nil {
		log.Fatal(err)
	}
	defer rows_handle.Close()

	rows, err := pgx.CollectRows(rows_handle, pgx.RowToMap)
	if err != nil {
		log.Fatal(err)
	}

	databases := lo.Map(
		lo.Filter(rows, func(item map[string]any, _ int) bool {
			return item["datname"].(string) != "cloudsqladmin"
		}),
		func(item map[string]any, _ int) *database {
			name := item["datname"].(string)
			var size int64
			var sizePretty string
			err := db.QueryRow(
				context.Background(),
				"SELECT pg_database_size($1), pg_size_pretty(pg_database_size($1))",
				name).Scan(&size, &sizePretty)
			if err != nil {
				log.Fatal(err)
			}

			return newDatabase(&d.connectionStringPattern, name, size, sizePretty)
		})

	d.databases = databases
}

func (d *DatabasesCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range d.databases {
		ch <- d.sizeMetric
		for _, table := range d.tables {
			ch <- table.sizeMetric
			ch <- table.rowsCountEstimateMetric
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
