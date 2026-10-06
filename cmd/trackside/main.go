// Command trackside ingests UK rail open data and serves it over HTTP.
package main

import (
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/compat"
	"github.com/carbonarok/trackside/internal/corpus"
	"github.com/carbonarok/trackside/internal/darwin"
	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/feeds"
	"github.com/carbonarok/trackside/internal/ldb"
	"github.com/carbonarok/trackside/internal/schedule"
	"github.com/carbonarok/trackside/internal/td"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

const usage = `trackside: open UK rail timetable and live running API

Usage:
  trackside serve                     run the API and live feed consumers
  trackside migrate                   apply database migrations
  trackside import-corpus [FILE]      load CORPUS reference data (downloads if no FILE)
  trackside import-schedule [FILE]    load a SCHEDULE file (downloads the full extract if no FILE)
  trackside import-smart [FILE]       load SMART TD berth data (downloads if no FILE)
  trackside import-darwin-ref FILE    load a Darwin reference data file (*_ref_v*.xml[.gz])
  trackside refresh-services          re-resolve services for the current window

Configuration is read from the environment; see README.md.
`

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})))
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := loadDotEnv(".env"); err != nil {
		slog.Error("reading .env", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, flag.Arg(0), flag.Args()[1:]); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

// loadDotEnv sets variables from a KEY=VALUE file, if present. Variables
// already in the environment win.
func loadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return nil
}

func logLevel() slog.Level {
	if os.Getenv("LOG_LEVEL") == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func run(ctx context.Context, cmd string, args []string) error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	nr := feedConfig()

	switch cmd {
	case "migrate":
		return nil
	case "import-corpus":
		return importCORPUS(ctx, pool, nr, args)
	case "import-schedule":
		return importSchedule(ctx, pool, nr, args)
	case "import-smart":
		return importSMART(ctx, pool, nr, args)
	case "import-darwin-ref":
		if len(args) == 0 {
			return errors.New("import-darwin-ref needs a file")
		}
		return importDarwinRef(ctx, pool, nr, args)
	case "refresh-services":
		return schedule.RefreshServices(ctx, pool)
	case "serve":
		return serve(ctx, pool, nr)
	}
	flag.Usage()
	return fmt.Errorf("unknown command %q", cmd)
}

// startFeed consumes a Network Rail feed from the Rail Data Marketplace when
// RDM_<name>_* is configured. Otherwise it adds the topic to stompTopics, which
// are all read over one connection to Network Rail's STOMP service.
func startFeed(ctx context.Context, stompTopics map[string]feeds.Handler, name, topic string, h feeds.Handler) {
	if k := kafkaConfig(name, topic); k.Enabled() {
		go consumeKafka(ctx, name, k, h)
		return
	}
	stompTopics[topic] = h
}

// kafkaConfig reads RDM_<name>_USERNAME, _PASSWORD, _GROUP and _TOPIC. The
// values come from the product's Pub/Sub page on raildata.org.uk.
func kafkaConfig(name, defaultTopic string) feeds.KafkaConfig {
	p := "RDM_" + name + "_"
	return feeds.KafkaConfig{
		Bootstrap: env(p+"BOOTSTRAP", env("RDM_BOOTSTRAP", feeds.DefaultKafkaBootstrap)),
		Username:  os.Getenv(p + "USERNAME"),
		Password:  os.Getenv(p + "PASSWORD"),
		Group:     os.Getenv(p + "GROUP"),
		Topic:     env(p+"TOPIC", defaultTopic),
	}
}

func consumeKafka(ctx context.Context, name string, k feeds.KafkaConfig, h feeds.Handler) {
	for ctx.Err() == nil {
		err := k.Consume(ctx, h)
		if errors.Is(err, feeds.ErrAuth) {
			slog.Error("Rail Data Marketplace rejected the credentials; not retrying. Check RDM_"+name+
				"_USERNAME, _PASSWORD and _GROUP against the product's Pub/Sub page, then restart.",
				"feed", name, "err", err)
			return
		}
		if err != nil {
			slog.Warn("kafka consumer stopped; restarting", "feed", name, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
}

func importSMART(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config, args []string) error {
	r, err := open(ctx, nr, args, feeds.SMARTPath)
	if err != nil {
		return err
	}
	defer r.Close()
	berths, err := td.ParseSMART(r)
	if err != nil {
		return err
	}
	if err := td.LoadSMART(ctx, pool, berths); err != nil {
		return err
	}
	slog.Info("loaded SMART", "berths", len(berths))
	return nil
}

func importDarwinRef(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config, args []string) error {
	r, err := open(ctx, nr, args, "")
	if err != nil {
		return err
	}
	defer r.Close()
	ref, err := darwin.ParseReference(r)
	if err != nil {
		return err
	}
	if err := darwin.LoadReference(ctx, pool, ref); err != nil {
		return err
	}
	slog.Info("loaded Darwin reference data", "locations", len(ref.Locations), "operators", len(ref.TOCs),
		"late_reasons", len(ref.LateReasons), "cancel_reasons", len(ref.CancelReasons))
	return nil
}

func feedConfig() feeds.Config {
	host, _ := os.Hostname()
	return feeds.Config{
		Username:  os.Getenv("NR_USERNAME"),
		Password:  os.Getenv("NR_PASSWORD"),
		StompAddr: env("NR_STOMP_ADDR", "publicdatafeeds.networkrail.co.uk:61618"),
		FilesURL:  env("NR_FILES_URL", "https://publicdatafeeds.networkrail.co.uk"),
		ClientID:  env("NR_CLIENT_ID", "trackside-"+host),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// open returns a reader over a local file (gunzipping .gz files) or, when no
// file is given, over a download from Network Rail.
func open(ctx context.Context, nr feeds.Config, args []string, path string) (io.ReadCloser, error) {
	if len(args) == 0 {
		if nr.Username == "" {
			return nil, errors.New("no file given and NR_USERNAME is not set")
		}
		slog.Info("downloading", "path", path)
		return nr.Download(ctx, path)
	}
	f, err := os.Open(args[0])
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(args[0], ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, f}, nil
}

func importCORPUS(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config, args []string) error {
	r, err := open(ctx, nr, args, feeds.CORPUSPath)
	if err != nil {
		return err
	}
	defer r.Close()
	entries, err := corpus.Parse(r)
	if err != nil {
		return err
	}
	n, err := corpus.Load(ctx, pool, entries)
	if err != nil {
		return err
	}
	slog.Info("loaded CORPUS", "locations", n)
	return nil
}

func importSchedule(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config, args []string) error {
	r, err := open(ctx, nr, args, feeds.ScheduleFullPath)
	if err != nil {
		return err
	}
	defer r.Close()
	start := time.Now()
	res, err := schedule.Load(ctx, pool, r)
	if err != nil {
		return err
	}
	slog.Info("loaded schedule", "type", res.Type, "sequence", res.Sequence, "schedules", res.Schedules,
		"deletes", res.Deletes, "tiplocs", res.TIPLOCs, "associations", res.Associations,
		"took", time.Since(start).Round(time.Second))
	if err := schedule.RefreshServices(ctx, pool); err != nil {
		return err
	}
	slog.Info("services resolved")
	return nil
}

func serve(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config) error {
	store := &timetable.Store{Pool: pool}
	darwinStream := kafkaConfig("DARWIN", "prod-1010-Darwin-Train-Information-Push-Port-IIII2_0-JSON")
	if token := os.Getenv("NRE_LDBWS_TOKEN"); token != "" && !darwinStream.Enabled() {
		lite := ldb.New(token)
		lite.URL = os.Getenv("NRE_LDBWS_URL")
		store.Live = lite
		slog.Info("using Darwin Lite for live boards (on demand, cached)")
	}
	mux := http.NewServeMux()
	(&api.Server{Store: store}).Register(mux)
	(&compat.Server{Store: store}).Register(mux)
	api.RegisterDocs(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	go every(ctx, time.Hour, "refresh services", func(ctx context.Context) error {
		return schedule.RefreshServices(ctx, pool)
	})
	if nr.Username != "" {
		go every(ctx, time.Hour, "daily schedule update", func(ctx context.Context) error {
			return dailyUpdate(ctx, pool, nr)
		})
	}

	stompTopics := map[string]feeds.Handler{}
	trustApplier := &trust.Applier{Pool: pool}
	startFeed(ctx, stompTopics, "TRUST", "TRAIN_MVT_ALL_TOC", trustApplier.ApplyFrame)
	startFeed(ctx, stompTopics, "VSTP", "VSTP_ALL", func(ctx context.Context, body []byte) error {
		rec, err := schedule.ParseVSTP(body)
		if err != nil {
			return err
		}
		return schedule.ApplyVSTP(ctx, pool, rec, time.Now())
	})
	smart, n, err := td.LoadMap(ctx, pool)
	if err != nil {
		return err
	}
	if n > 0 {
		proc := &td.Processor{Pool: pool, Map: smart}
		startFeed(ctx, stompTopics, "TD", "TD_ALL_SIG_AREA", proc.ApplyFrame)
	} else {
		slog.Info("no SMART data loaded; Train Describer disabled (run import-smart)")
	}
	if len(stompTopics) > 0 {
		if nr.Username != "" {
			go nr.Subscribe(ctx, stompTopics)
		} else {
			slog.Warn("no live Network Rail feeds: set NR_USERNAME/NR_PASSWORD or RDM_<FEED>_* (see README)")
		}
	}
	darwinApplier := &darwin.Applier{Pool: pool}
	switch {
	case darwinStream.Enabled():
		go consumeKafka(ctx, "Darwin", darwinStream, darwinApplier.ApplyMessage)
	case store.Live == nil:
		slog.Info("Darwin not configured (RDM_DARWIN_* or NRE_LDBWS_TOKEN); using TRUST-based estimates only")
	}

	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", ":8080"),
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// dailyUpdate applies the latest SCHEDULE update once it has been published.
// The file named for yesterday becomes available at around 06:00, so it is
// attempted hourly from then until it succeeds. The sequence check in
// schedule.Load stops a file being applied twice.
func dailyUpdate(ctx context.Context, pool *pgxpool.Pool, nr feeds.Config) error {
	now := time.Now().In(ukrail.London)
	today := now.Format(time.DateOnly)
	if now.Hour() < 6 {
		return nil
	}
	done, err := db.GetState(ctx, pool, "schedule_update_date")
	if err != nil || done == today {
		return err
	}
	if seq, _ := db.GetState(ctx, pool, "schedule_sequence"); seq == "" {
		return nil // no full timetable yet; the operator must run import-schedule
	}
	r, err := nr.Download(ctx, feeds.ScheduleUpdatePath(now.AddDate(0, 0, -1).Weekday()))
	if err != nil {
		return err
	}
	defer r.Close()
	res, err := schedule.Load(ctx, pool, r)
	if err != nil {
		return err
	}
	slog.Info("applied schedule update", "sequence", res.Sequence, "schedules", res.Schedules, "deletes", res.Deletes)
	if err := schedule.RefreshServices(ctx, pool); err != nil {
		return err
	}
	return db.SetState(ctx, pool, "schedule_update_date", today)
}

// every runs fn now and then at each interval until ctx is cancelled.
func every(ctx context.Context, interval time.Duration, name string, fn func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			slog.Warn(name+" failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
	})
}
