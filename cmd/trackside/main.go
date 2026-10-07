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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/activity"
	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/apns"
	"github.com/carbonarok/trackside/internal/compat"
	"github.com/carbonarok/trackside/internal/corpus"
	"github.com/carbonarok/trackside/internal/darwin"
	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/feeds"
	"github.com/carbonarok/trackside/internal/history"
	"github.com/carbonarok/trackside/internal/inbox"
	"github.com/carbonarok/trackside/internal/ldb"
	"github.com/carbonarok/trackside/internal/live"
	"github.com/carbonarok/trackside/internal/naptan"
	"github.com/carbonarok/trackside/internal/schedule"
	"github.com/carbonarok/trackside/internal/td"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
	"github.com/carbonarok/trackside/web"
)

const usage = `trackside: open UK rail timetable and live running API

Usage:
  trackside serve                     run the API and live feed consumers
  trackside migrate                   apply database migrations
  trackside import-corpus [FILE]      load CORPUS reference data (downloads if no FILE)
  trackside import-schedule [FILE]    load a SCHEDULE file (downloads the full extract if no FILE)
  trackside import-smart [FILE]       load SMART TD berth data (downloads if no FILE)
  trackside import-darwin-ref FILE    load a Darwin reference data file (*_ref_v*.xml[.gz])
  trackside import-naptan [FILE]      load station coordinates from NaPTAN (downloads if no FILE)
  trackside import-inbox              import new files from INBOX_DIR / INBOX_BUCKET once
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
	case "import-naptan":
		return importNaPTAN(ctx, pool, args)
	case "import-inbox":
		importers, err := inboxImporters(pool)
		if err != nil {
			return err
		}
		if len(importers) == 0 {
			return errors.New("set INBOX_DIR or INBOX_BUCKET")
		}
		for _, im := range importers {
			if err := im.Poll(ctx); err != nil {
				return err
			}
		}
		return nil
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

func importNaPTAN(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	var r io.ReadCloser
	var err error
	if len(args) > 0 {
		r, err = os.Open(args[0])
	} else {
		slog.Info("downloading", "url", naptan.DownloadURL)
		r, err = naptan.Download(ctx)
	}
	if err != nil {
		return err
	}
	defer r.Close()
	stations, err := naptan.Parse(r)
	if err != nil {
		return err
	}
	n, err := naptan.Load(ctx, pool, stations)
	if err != nil {
		return err
	}
	slog.Info("loaded NaPTAN", "stations", len(stations), "locations_with_coordinates", n)
	return nil
}

// inboxImporters builds importers for INBOX_DIR, INBOX_BUCKET and INBOX_SFTP.
func inboxImporters(pool *pgxpool.Pool) ([]*inbox.Importer, error) {
	var out []*inbox.Importer
	if dir := os.Getenv("INBOX_DIR"); dir != "" {
		out = append(out, &inbox.Importer{Pool: pool, Source: inbox.Dir{Path: dir}})
	}
	if u := os.Getenv("INBOX_BUCKET"); u != "" {
		b, err := inbox.NewBucket(u, os.Getenv("INBOX_ENDPOINT"), os.Getenv("INBOX_ACCESS_KEY"), os.Getenv("INBOX_SECRET_KEY"))
		if err != nil {
			return nil, err
		}
		out = append(out, &inbox.Importer{Pool: pool, Source: b})
	}
	if u := os.Getenv("INBOX_SFTP"); u != "" {
		s, err := inbox.NewSFTP(u, os.Getenv("INBOX_SFTP_PASSWORD"), os.Getenv("INBOX_SFTP_KEY"), os.Getenv("INBOX_SFTP_HOST_KEY"))
		if err != nil {
			return nil, err
		}
		out = append(out, &inbox.Importer{Pool: pool, Source: s})
	}
	return out, nil
}

// historyKeep reads HISTORY_DAYS (default 400; 0 keeps history forever).
func historyKeep() time.Duration {
	days := 400
	if v := os.Getenv("HISTORY_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

func inboxInterval() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("INBOX_INTERVAL")); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
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
	// Browsers learn what changed over a WebSocket and refetch only then.
	// Darwin Lite boards are fetched on demand and never pushed, so clients
	// keep polling those more often.
	hub := live.New(live.NewResolver(pool))
	if store.Live != nil {
		hub.Fallback = 30
	}

	// iOS Live Activities are pushed through APNs when a key is configured.
	activities := &activity.Server{Pool: pool, Store: store, Bundles: bundleIDs(os.Getenv("APNS_BUNDLE_IDS"))}
	apnsClient, apnsHost, err := apnsFromEnv()
	if err != nil {
		return err
	}
	if apnsClient != nil {
		pusher := activity.NewPusher(pool, store, apnsClient, apnsHost)
		hub.OnChange = pusher.Notify
		go pusher.Run(ctx)
		activities.Enabled = true
		slog.Info("live activity pushes on", "apns", apnsHost)
	}
	go hub.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/live", hub)
	activities.Register(mux)
	hist := &history.Querier{Pool: pool, Store: store}
	(&api.Server{Store: store, History: hist}).Register(mux)
	(&compat.Server{Store: store}).Register(mux)
	api.RegisterDocs(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	// The web frontend takes every path the API doesn't.
	mux.Handle("GET /", web.Handler(web.Config{
		TileURL:         os.Getenv("MAP_TILE_URL"),
		TileAttribution: os.Getenv("MAP_TILE_ATTRIBUTION"),
	}))

	go every(ctx, time.Hour, "refresh services", func(ctx context.Context) error {
		return schedule.RefreshServices(ctx, pool)
	})
	archiver := &history.Archiver{Pool: pool, Store: store, Keep: historyKeep()}
	go every(ctx, time.Hour, "archive history", archiver.Run)
	if nr.Username != "" {
		go every(ctx, time.Hour, "daily schedule update", func(ctx context.Context) error {
			return dailyUpdate(ctx, pool, nr)
		})
	}

	stompTopics := map[string]feeds.Handler{}
	trustApplier := &trust.Applier{Pool: pool, Live: hub}
	startFeed(ctx, stompTopics, "TRUST", "TRAIN_MVT_ALL_TOC", trustApplier.ApplyFrame)
	startFeed(ctx, stompTopics, "VSTP", "VSTP_ALL", func(ctx context.Context, body []byte) error {
		rec, err := schedule.ParseVSTP(body)
		if err != nil {
			return err
		}
		if err := schedule.ApplyVSTP(ctx, pool, rec, time.Now()); err != nil {
			return err
		}
		switch {
		case rec.Schedule != nil:
			hub.UID(rec.Schedule.TrainUID)
		case rec.Delete != nil:
			hub.UID(rec.Delete.TrainUID)
		}
		return nil
	})
	smart, n, err := td.LoadMap(ctx, pool)
	if err != nil {
		return err
	}
	tdProc := &td.Processor{Pool: pool, Map: smart, Live: hub}
	var tdMu sync.Mutex
	tdStarted := n > 0
	if tdStarted {
		startFeed(ctx, stompTopics, "TD", "TD_ALL_SIG_AREA", tdProc.ApplyFrame)
	} else {
		slog.Info("no SMART data loaded; Train Describer starts once it is (import-smart or the inbox)")
	}

	// Station coordinates are public (NaPTAN), so keep them current without
	// any configuration: weekly, and straight away if there are none. On a
	// new database the stations they attach to arrive with CORPUS from the
	// inbox, so until some stick, try again every 10 minutes.
	go func() {
		withCoordinates := func() int {
			var n int
			pool.QueryRow(ctx, `SELECT count(*) FROM locations WHERE lat IS NOT NULL`).Scan(&n)
			return n
		}
		wait := time.Duration(0)
		if withCoordinates() > 0 {
			wait = 7 * 24 * time.Hour
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := importNaPTAN(ctx, pool, nil); err != nil && ctx.Err() == nil {
				slog.Warn("station coordinates failed", "err", err)
			}
			wait = 7 * 24 * time.Hour
			if withCoordinates() == 0 {
				wait = 10 * time.Minute
			}
		}
	}()

	importers, err := inboxImporters(pool)
	if err != nil {
		return err
	}
	for _, im := range importers {
		im.OnSMART = func(m *td.Map) {
			tdProc.SetMap(m)
			tdMu.Lock()
			defer tdMu.Unlock()
			if tdStarted {
				return
			}
			if k := kafkaConfig("TD", "TD_ALL_SIG_AREA"); k.Enabled() {
				tdStarted = true
				go consumeKafka(ctx, "TD", k, tdProc.ApplyFrame)
				return
			}
			slog.Info("SMART loaded; restart trackside to start Train Describer over STOMP")
		}
		slog.Info("watching inbox", "source", im.Source)
		go every(ctx, inboxInterval(), "inbox "+im.Source.String(), im.Poll)
	}
	if len(stompTopics) > 0 {
		if nr.Username != "" {
			go nr.Subscribe(ctx, stompTopics)
		} else {
			slog.Warn("no live Network Rail feeds: set NR_USERNAME/NR_PASSWORD or RDM_<FEED>_* (see README)")
		}
	}
	darwinApplier := &darwin.Applier{Pool: pool, Live: hub}
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

// apnsFromEnv builds the APNs client from APNS_KEY_FILE (the .p8 file) or
// APNS_KEY (its contents; "\n" may stand for line breaks so it fits on one
// line of .env), APNS_KEY_ID and APNS_TEAM_ID. APNS_ENV picks the environment
// tried first: production (the default) or sandbox. No key, no client.
func apnsFromEnv() (*apns.Client, string, error) {
	keyPEM := os.Getenv("APNS_KEY")
	if f := os.Getenv("APNS_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, "", fmt.Errorf("APNS_KEY_FILE: %w", err)
		}
		keyPEM = string(b)
	}
	if keyPEM == "" {
		return nil, "", nil
	}
	c, err := apns.New([]byte(strings.ReplaceAll(keyPEM, `\n`, "\n")), os.Getenv("APNS_KEY_ID"), os.Getenv("APNS_TEAM_ID"))
	if err != nil {
		return nil, "", fmt.Errorf("APNs: %w (set APNS_KEY_ID and APNS_TEAM_ID with the key)", err)
	}
	switch env := os.Getenv("APNS_ENV"); env {
	case "", "production":
		return c, apns.Production, nil
	case "sandbox", "development":
		return c, apns.Sandbox, nil
	default:
		return nil, "", fmt.Errorf("APNS_ENV must be production or sandbox, not %q", env)
	}
}

// bundleIDs parses a comma-separated allowlist; empty allows any.
func bundleIDs(s string) map[string]bool {
	out := map[string]bool{}
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out[b] = true
		}
	}
	return out
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
