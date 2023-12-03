# Foxpost watcher

[![Build Status](https://drone.k8s.marcsello.com/api/badges/marcsello/foxpost-watcher/status.svg)](https://drone.k8s.marcsello.com/marcsello/foxpost-watcher)

Just a simple go program to track the load of package lockers using Foxpost's public API, and record this data in InfluxDB.

## Get

```
docker run marcsello/foxpost-watcher
```

## Config

Configurable trough envvars:

| envvar                   | default   | description                                                                                                    |
|--------------------------|-----------|----------------------------------------------------------------------------------------------------------------|
| `INVOCATION_TIMEOUT`     | `1m`      | Total timeout for an invocation (collecting, parsing and submitting together)                                  |
| `FOXPOST_PLACE_IDS`      |           | Comma separated `place_id`s (see Foxpost API to get those)                                                     |
| `INFLUX_SERVER_URL`      |           | Url of your InfluxDB instance                                                                                  |
| `INFLUX_SERVER_TOKEN`    |           | API token for your InfluxDB instance                                                                           |
| `INFLUX_SERVER_ORG`      |           | InfluxDB Organization                                                                                          |
| `INFLUX_SERVER_BUCKET`   |           | InfluxDB Bucket                                                                                                |
| `INFLUX_SERVER_EXTRA_CA` |           | Extra CA cert in PEM format (used only for influxdb communication)                                             |
| `INFLUX_MEASUREMENT`     | `foxpost` | Name of the measurement to write the data in                                                                   |
| `POLL_INTERVAL`          | `1h`      | Interval between invocations. Foxpost updates their data hourly, so there is no point setting shorter interval |
