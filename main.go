package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/go-retryablehttp"
	influxdb2 "github.com/influxdata/influxdb-client-go"
	"github.com/influxdata/influxdb-client-go/api/write"
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
	dryRun            bool
}

func (ic *InstanceConfig) GetWriter() func(context.Context, *write.Point) error {

	if ic.dryRun {
		return func(_ context.Context, point *write.Point) error {
			tagsStr := ""
			fieldsStr := ""
			for _, tag := range point.TagList() {
				tagsStr += fmt.Sprintf("%s=%s ", tag.Key, tag.Value)
			}
			for _, field := range point.FieldList() {
				fieldsStr += fmt.Sprintf("%s=%+v ", field.Key, field.Value)
			}
			log.Printf("[DRY RUN]: Would write datapoint: %s %s %+v", tagsStr, fieldsStr, point.Time())
			return nil
		}
	} else {
		// Prepare the write api, because we are going to write some serious stuff now.
		writeAPI := ic.influxClient.WriteAPIBlocking(ic.influxOrg, ic.influxBucket)
		return func(ctx context.Context, point *write.Point) error {
			return writeAPI.WritePoint(ctx, point)
		}
	}

}

// https://foxpost.hu/uzleti-partnereknek/integracios-segedlet/webapi-integracio#api-4
type APMData struct {
	// we only interested in these fields
	PlaceID    uint64  `json:"place_id"`
	OperatorID string  `json:"operator_id"`
	Name       string  `json:"name"`
	GeoLat     float64 `json:"geolat"`
	GeoLng     float64 `json:"geolng"`
	Load       string  `json:"load"`
}

var loadMap = map[string]uint8{
	// not sure if those two are the same, but they appear similar on the map
	"":              10,
	"normal loaded": 10,
	"medium loaded": 70,
	"overloaded":    100,
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

	dryRun := env.Bool("DRY_RUN", false)

	influxOrg := ""
	influxBucket := ""
	var influxClient influxdb2.Client

	if !dryRun {
		log.Println("Setting up influxdb client...")
		influxOrg = env.StringOrPanic("INFLUX_SERVER_ORG")
		influxBucket = env.StringOrPanic("INFLUX_SERVER_BUCKET")

		const extraCAEnvvarName = "INFLUX_SERVER_EXTRA_CA"
		clientOpts := influxdb2.DefaultOptions()
		if env.Exists(extraCAEnvvarName) {
			log.Println("Loading extra CA cert from envvar...")
			// get the current cert pool, or a new one
			rootCAs, _ := x509.SystemCertPool()
			if rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}

			// append our cert
			rootCAs.AppendCertsFromPEM([]byte(env.StringOrPanic(extraCAEnvvarName)))

			// set it in the client options
			clientOpts = clientOpts.SetTLSConfig(&tls.Config{
				RootCAs:    rootCAs,
				MinVersion: tls.VersionTLS12, // just to make gosec happy
			})
		}

		influxClient = influxdb2.NewClientWithOptions(
			env.StringOrPanic("INFLUX_SERVER_URL"),
			env.StringOrPanic("INFLUX_SERVER_TOKEN"),
			clientOpts,
		)

		hc, err := influxClient.Health(context.Background())
		if err != nil {
			panic("influxdb health check failed")
		}
		log.Println("InfluxDB initial health check result: ", hc.Status)
	} else {
		log.Println("Dry run enabled! Not setting up Influx Client")
	}

	return &InstanceConfig{
		timeout:           env.Duration("INVOCATION_TIMEOUT", time.Minute),
		placeIDs:          placeIDs,
		influxClient:      influxClient,
		influxOrg:         influxOrg,
		influxBucket:      influxBucket,
		influxMeasurement: env.String("INFLUX_MEASUREMENT", "foxpost"),
		dryRun:            dryRun,
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

	writer := ic.GetWriter()

	for _, apmData := range apmsData {
		if slices.Contains(ic.placeIDs, apmData.PlaceID) {
			// this is a place of interest. Record its status
			log.Printf("Found place %d", apmData.PlaceID)

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

			err = writer(ctx, p)
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
