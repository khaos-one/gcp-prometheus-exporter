#!/usr/bin/env sh
docker build --platform "linux/amd64" -t khaosl33t/pgsql-prometheus-exporter:1.0.1 .
docker push khaosl33t/pgsql-prometheus-exporter:1.0.1
