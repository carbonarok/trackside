package e2e

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/inbox"
	"github.com/carbonarok/trackside/internal/td"
)

func copyFile(t *testing.T, src, dst string, gz bool) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var w io.Writer = out
	if gz {
		z := gzip.NewWriter(out)
		defer z.Close()
		w = z
	}
	if _, err := io.Copy(w, in); err != nil {
		t.Fatal(err)
	}
}

// updateWithSequence writes a copy of the fixture update file renumbered.
func updateWithSequence(t *testing.T, dst string, seq int) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/schedule_update.json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(b), "\n", 2)
	var hdr map[string]map[string]any
	json.Unmarshal([]byte(lines[0]), &hdr)
	hdr["JsonTimetableV1"]["Metadata"].(map[string]any)["sequence"] = seq
	first, _ := json.Marshal(hdr)
	if err := os.WriteFile(dst, []byte(string(first)+"\n"+lines[1]), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInbox(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	copyFile(t, "../../testdata/corpus.json", filepath.Join(dir, "CORPUSExtract.json"), false)
	copyFile(t, "../../testdata/smart.json", filepath.Join(dir, "SMARTExtract.json.gz"), true)
	// Darwin delivers into its own folder, with several versions of each
	// reference file; the newest version must win.
	os.Mkdir(filepath.Join(dir, "PPTimetable"), 0o755)
	copyFile(t, "../../testdata/darwin_ref.xml", filepath.Join(dir, "PPTimetable", "20261006020500_ref_v4.xml.gz"), true)
	os.WriteFile(filepath.Join(dir, "PPTimetable", "20261006020500_ref_v2.xml.gz"), []byte("not gzip"), 0o644)
	// An older publication delivered later must not win.
	os.WriteFile(filepath.Join(dir, "PPTimetable", "20260930020500_ref_v4.xml.gz"), []byte("not gzip"), 0o644)
	now := time.Now()
	os.Chtimes(filepath.Join(dir, "PPTimetable", "20261006020500_ref_v4.xml.gz"), now, now)
	os.Chtimes(filepath.Join(dir, "PPTimetable", "20261006020500_ref_v2.xml.gz"), now, now)
	os.Chtimes(filepath.Join(dir, "PPTimetable", "20260930020500_ref_v4.xml.gz"), now.Add(time.Minute), now.Add(time.Minute))
	copyFile(t, "../../testdata/schedule_full.json", filepath.Join(dir, "CIF_ALL_FULL_DAILY_toc-full.json.gz"), true)
	updateWithSequence(t, filepath.Join(dir, "CIF_ALL_UPDATE_DAILY_toc-update-mon.json"), 101)
	os.WriteFile(filepath.Join(dir, "CIF_ALL_FULL_DAILY_toc-full.CIF.gz"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)

	var smart *td.Map
	im := &inbox.Importer{Pool: pool, Source: inbox.Dir{Path: dir}, OnSMART: func(m *td.Map) { smart = m }}
	if err := im.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	count := func(sql string) int {
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM schedules WHERE train_uid = 'W19999'`); n != 1 {
		t.Errorf("update after the full file not applied (W19999 schedules = %d)", n)
	}
	var seq string
	pool.QueryRow(ctx, `SELECT value FROM feed_state WHERE feed = 'schedule_sequence'`).Scan(&seq)
	if seq != "101" {
		t.Errorf("schedule sequence = %s, want 101", seq)
	}
	if count(`SELECT count(*) FROM smart_berths`) != 5 || smart == nil {
		t.Errorf("SMART not loaded or not handed to Train Describer")
	}
	if count(`SELECT count(*) FROM darwin_reasons`) != 2 {
		t.Errorf("Darwin reference data not loaded")
	}
	if count(`SELECT count(*) FROM services`) == 0 {
		t.Errorf("services not resolved after import")
	}
	if n := count(`SELECT count(*) FROM inbox_files WHERE result = 'loaded'`); n != 5 {
		t.Errorf("loaded files = %d, want 5", n)
	}
	if n := count(`SELECT count(*) FROM inbox_files WHERE name = 'PPTimetable/20261006020500_ref_v4.xml.gz' AND result = 'loaded'`); n != 1 {
		t.Errorf("Darwin ref_v4 in a sub-folder was not the one loaded")
	}
	// Five loaded, plus two superseded Darwin files; CIF and unrelated files
	// ignored.
	if n := count(`SELECT count(*) FROM inbox_files`); n != 7 {
		t.Errorf("recorded = %d, want 7", n)
	}

	// A second poll finds nothing new.
	if err := im.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM inbox_files`); n != 7 {
		t.Errorf("files reprocessed: %d records", n)
	}

	// An update that skips a sequence number waits rather than being applied.
	updateWithSequence(t, filepath.Join(dir, "CIF_ALL_UPDATE_DAILY_toc-update-wed.json"), 103)
	if err := im.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(ctx, `SELECT value FROM feed_state WHERE feed = 'schedule_sequence'`).Scan(&seq)
	if seq != "101" {
		t.Errorf("out-of-order update applied: sequence = %s", seq)
	}
	// Once the missing one arrives, both apply in order.
	updateWithSequence(t, filepath.Join(dir, "CIF_ALL_UPDATE_DAILY_toc-update-tue.json"), 102)
	if err := im.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(ctx, `SELECT value FROM feed_state WHERE feed = 'schedule_sequence'`).Scan(&seq)
	if seq != "103" {
		t.Errorf("updates not applied in order: sequence = %s, want 103", seq)
	}
}

func TestClassify(t *testing.T) {
	for name, want := range map[string]string{
		"CIF_ALL_FULL_DAILY_toc-full.json.gz":                      inbox.KindScheduleFull,
		"nwr-schedule/CIF_ALL_UPDATE_DAILY_toc-update-mon.json.gz": inbox.KindScheduleUpdate,
		"CIF_ALL_UPDATE_DAILY_toc-update-mon.CIF.gz":               "",
		"CORPUSExtract.json.gz":                                    inbox.KindCORPUS,
		"CORPUSExtract.csv.gz":                                     "",
		"SMARTExtract.json.gz":                                     inbox.KindSMART,
		"20261006020500_ref_v4.xml.gz":                             inbox.KindDarwinRef,
		"20261006020500_v8.xml.gz":                                 "",
	} {
		if got := inbox.Classify(name); got != want {
			t.Errorf("Classify(%q) = %q, want %q", name, got, want)
		}
	}
}
