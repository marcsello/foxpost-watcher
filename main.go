package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/go-retryablehttp"
	influxdb2 "github.com/influxdata/influxdb-client-go"
	"gitlab.com/MikeTTh/env"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type InstanceConfig struct {
	timeout           time.Duration
	placeIDs          []uint64
	influxClient      influxdb2.Client
	influxOrg         string
	influxBucket      string
	influxMeasurement string
}

type APMData struct {
	// we only interested in these fields
	PlaceID    uint64  `json:"place_id"`
	OperatorID string  `json:"operator_id"`
	Name       string  `json:"name"`
	GeoLat     float64 `json:"geolat"`
	GeoLng     float64 `json:"geolng"`
	Load       string  `json:"load"`
}

var loadMap = map[string]float32{
	"normal loaded": 0.3,
	"medium loaded": 0.7,
	"overloaded":    1,
}

func loadConfig() *InstanceConfig {

	placeIDsStr := env.StringOrPanic("FOXPOST_PLACE_IDS")
	placeIDsStrs := strings.Split(placeIDsStr, ",")
	if len(placeIDsStrs) == 0 {
		panic("no place ids?")
	}

	placeIDs := make([]uint64, len(placeIDsStrs))
	for i, v := range placeIDsStrs {
		placeID, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			panic("invalid place id")
		}
		placeIDs[i] = placeID
	}

	clientOpts := influxdb2.DefaultOptions()
	if env.Exists("INFLUX_SERVER_EXTRA_CA") {
		log.Println("Loading extra CA cert from envvar...")
		// get the current cert pool, or a new one
		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}

		// append our cert
		rootCAs.AppendCertsFromPEM([]byte(env.StringOrPanic("INFLUX_SERVER_CA")))

		// set it in the client options
		clientOpts = clientOpts.SetTLSConfig(&tls.Config{
			RootCAs:    rootCAs,
			MinVersion: tls.VersionTLS12, // just to make gosec happy
		})
	}

	influxClient := influxdb2.NewClientWithOptions(
		env.StringOrPanic("INFLUX_SERVER_URL"),
		env.StringOrPanic("INFLUX_SERVER_TOKEN"),
		clientOpts,
	)

	hc, err := influxClient.Health(context.Background())
	if err != nil {
		panic("influxdb health check failed")
	}
	log.Println("InfluxDB initial health check result: ", hc.Status)

	return &InstanceConfig{
		timeout:           env.Duration("INVOCATION_TIMEOUT", time.Minute),
		placeIDs:          placeIDs,
		influxClient:      influxClient,
		influxOrg:         env.StringOrPanic("INFLUX_SERVER_ORG"),
		influxBucket:      env.StringOrPanic("INFLUX_SERVER_BUCKET"),
		influxMeasurement: env.String("INFLUX_MEASUREMENT", "foxpost"),
	}
}

func run(ctx context.Context, ic *InstanceConfig) error {
	var err error

	cl := retryablehttp.NewClient()

	var req *retryablehttp.Request
	req, err = retryablehttp.NewRequestWithContext(ctx, http.MethodGet, "https://cdn.foxpost.hu/apms.json", nil)
	if err != nil {
		return err
	}

	var resp *http.Response
	resp, err = cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ts := time.Now() // record the time of the successful request

	// this is "slipped" through the retrier
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	// cool and good, parse response
	var apmsData []APMData
	err = json.NewDecoder(resp.Body).Decode(&apmsData)
	if err != nil {
		return err
	}

	// Prepare the write api, because we are going to write some serious stuff now.
	writeAPI := ic.influxClient.WriteAPIBlocking(ic.influxOrg, ic.influxBucket)

	for _, apmData := range apmsData {
		if slices.Contains(ic.placeIDs, apmData.PlaceID) {
			// this is a place of interest. Record its status

			loadVal, ok := loadMap[apmData.Load]
			if !ok {
				return fmt.Errorf("invalid load value: %s", apmData.Load)
			}

			tags := map[string]string{
				"place_id":    strconv.FormatUint(apmData.PlaceID, 10),
				"operator_id": apmData.OperatorID,
				"name":        apmData.Name,
			}

			fields := map[string]interface{}{
				"load":   loadVal,
				"geoLat": apmData.GeoLat,
				"geoLng": apmData.GeoLng,
			}

			p := influxdb2.NewPoint(ic.influxMeasurement, tags, fields, ts)

			err = writeAPI.WritePoint(ctx, p)
			if err != nil {
				return err
			}

		}
		// check if context is closed every iteration
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

func invoke(ic *InstanceConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), ic.timeout)
	defer cancel()
	return run(ctx, ic)
}

func safeInvoke(ic *InstanceConfig) {
	// Used by the daemon, so if won't crash
	defer func() {
		if r := recover(); r != nil {
			log.Println("PANIC! ", r, " (recovered)")
		}
	}()

	err := invoke(ic)
	if err != nil {
		log.Println("Error while running collection: ", err)
		return
	}
}

func daemon(ic *InstanceConfig) {
	log.Println("Starting ticker...")
	ticker := time.NewTicker(env.Duration("POLL_INTERVAL", time.Hour))

	for range ticker.C {
		log.Println("Tick!")
		safeInvoke(ic)
	}
}

func main() {
	log.Println("Parsing config...")
	ic := loadConfig()

	oneShot := env.Bool("ONESHOT", false)
	if oneShot {
		// run once, crash on failure
		log.Println("Running in one-shot mode...")
		err := invoke(ic)
		if err != nil {
			panic(err)
		}
	} else {
		// run as daemon, protected from crashing
		log.Println("Running as daemon...")
		safeInvoke(ic)
		daemon(ic)
	}

}
