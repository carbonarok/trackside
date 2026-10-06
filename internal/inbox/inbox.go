package inbox

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/corpus"
	"github.com/carbonarok/trackside/internal/darwin"
	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/schedule"
	"github.com/carbonarok/trackside/internal/td"
)

// Kinds of file the inbox understands.
const (
	KindCORPUS         = "corpus"
	KindSMART          = "smart"
	KindDarwinRef      = "darwin-ref"
	KindScheduleFull   = "schedule-full"
	KindScheduleUpdate = "schedule-update"
)

// Classify works out what a file is from its name, or "" to ignore it.
// Network Rail's CIF-format copies (*.CIF.gz) are ignored in favour of the
// JSON ones.
func Classify(name string) string {
	base := strings.ToLower(path.Base(name))
	switch {
	case strings.Contains(base, ".cif"):
		return ""
	case strings.HasPrefix(base, "cif_all_full_daily") && strings.Contains(base, ".json"):
		return KindScheduleFull
	case strings.HasPrefix(base, "cif_all_update_daily") && strings.Contains(base, ".json"):
		return KindScheduleUpdate
	case strings.Contains(base, "corpus") && strings.Contains(base, ".json"):
		return KindCORPUS
	case strings.Contains(base, "smart") && strings.Contains(base, ".json"):
		return KindSMART
	case strings.Contains(base, "_ref_v") && strings.Contains(base, ".xml"):
		return KindDarwinRef
	}
	return ""
}

// Importer processes new files from a source.
type Importer struct {
	Pool   *pgxpool.Pool
	Source Source
	// OnSMART is called with freshly loaded SMART data, so a running Train
	// Describer consumer can start using it.
	OnSMART func(*td.Map)
}

type file struct {
	Object
	kind string
	seq  int // schedule files only
}

// Poll imports anything new. Reference data goes first, then the timetable:
// update files are applied in sequence, and the newest full file is loaded
// when there is no timetable yet or an update is missing.
func (im *Importer) Poll(ctx context.Context) error {
	objects, err := im.Source.List(ctx)
	if err != nil {
		return fmt.Errorf("list %s: %w", im.Source, err)
	}
	byKind := map[string][]file{}
	for _, o := range objects {
		kind := Classify(o.Name)
		if kind == "" {
			continue
		}
		done, err := im.seen(ctx, o)
		if err != nil {
			return err
		}
		if !done {
			byKind[kind] = append(byKind[kind], file{Object: o, kind: kind})
		}
	}

	// For reference data only the newest file matters.
	for _, kind := range []string{KindCORPUS, KindSMART, KindDarwinRef} {
		files := byKind[kind]
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Modified.Before(files[j].Modified) })
		newest := files[len(files)-1]
		im.record(ctx, newest, im.importReference(ctx, newest))
		for _, f := range files[:len(files)-1] {
			im.record(ctx, f, errSuperseded)
		}
	}
	return im.importSchedules(ctx, byKind[KindScheduleFull], byKind[KindScheduleUpdate])
}

var errSuperseded = errors.New("skipped: superseded by a newer file")

func (im *Importer) importReference(ctx context.Context, f file) error {
	r, err := im.open(ctx, f.Name)
	if err != nil {
		return err
	}
	defer r.Close()
	switch f.kind {
	case KindCORPUS:
		entries, err := corpus.Parse(r)
		if err != nil {
			return err
		}
		n, err := corpus.Load(ctx, im.Pool, entries)
		slog.Info("inbox: loaded CORPUS", "file", f.Name, "locations", n)
		return err
	case KindSMART:
		berths, err := td.ParseSMART(r)
		if err != nil {
			return err
		}
		if err := td.LoadSMART(ctx, im.Pool, berths); err != nil {
			return err
		}
		slog.Info("inbox: loaded SMART", "file", f.Name, "berths", len(berths))
		if im.OnSMART != nil {
			im.OnSMART(td.NewMap(berths))
		}
		return nil
	case KindDarwinRef:
		ref, err := darwin.ParseReference(r)
		if err != nil {
			return err
		}
		slog.Info("inbox: loaded Darwin reference data", "file", f.Name, "locations", len(ref.Locations))
		return darwin.LoadReference(ctx, im.Pool, ref)
	}
	return nil
}

func (im *Importer) importSchedules(ctx context.Context, fulls, updates []file) error {
	if len(fulls) == 0 && len(updates) == 0 {
		return nil
	}
	// Read each file's sequence number from its header.
	for _, list := range [][]file{fulls, updates} {
		for i := range list {
			seq, err := im.sequence(ctx, list[i].Name)
			if err != nil {
				im.record(ctx, list[i], err)
				list[i].seq = -1
				continue
			}
			list[i].seq = seq
		}
	}
	sort.Slice(fulls, func(i, j int) bool { return fulls[i].seq < fulls[j].seq })
	sort.Slice(updates, func(i, j int) bool { return updates[i].seq < updates[j].seq })

	cur, err := currentSequence(ctx, im.Pool)
	if err != nil {
		return err
	}
	changed := false
	handled := map[string]bool{}
	applyUpdates := func() {
		for _, u := range updates {
			if handled[u.Name] || u.seq < 0 {
				continue
			}
			switch {
			case u.seq <= cur:
				im.record(ctx, u, errors.New("skipped: already covered"))
				handled[u.Name] = true
			case u.seq == cur+1:
				err := im.loadSchedule(ctx, u)
				im.record(ctx, u, err)
				handled[u.Name] = true
				if err == nil {
					cur, changed = u.seq, true
				}
			}
		}
	}
	applyUpdates()

	// No timetable yet, or updates are missing: fall back to the newest full
	// file, then apply any updates after it.
	gap := false
	for _, u := range updates {
		if !handled[u.Name] && u.seq > cur+1 {
			gap = true
		}
	}
	if (cur == 0 || gap) && len(fulls) > 0 {
		newest := fulls[len(fulls)-1]
		if newest.seq > cur {
			err := im.loadSchedule(ctx, newest)
			im.record(ctx, newest, err)
			handled[newest.Name] = true
			if err == nil {
				cur, changed = newest.seq, true
				applyUpdates()
			}
		}
	}
	for _, f := range fulls {
		if !handled[f.Name] && f.seq >= 0 && f.seq <= cur {
			im.record(ctx, f, errors.New("skipped: timetable already up to date"))
		}
	}
	for _, u := range updates {
		if !handled[u.Name] && u.seq > cur+1 {
			slog.Warn("inbox: schedule update waiting for a missing earlier update or a full file",
				"file", u.Name, "sequence", u.seq, "current", cur)
		}
	}
	if changed {
		return schedule.RefreshServices(ctx, im.Pool)
	}
	return nil
}

func (im *Importer) loadSchedule(ctx context.Context, f file) error {
	r, err := im.open(ctx, f.Name)
	if err != nil {
		return err
	}
	defer r.Close()
	res, err := schedule.Load(ctx, im.Pool, r)
	if err != nil {
		return err
	}
	slog.Info("inbox: loaded schedule", "file", f.Name, "type", res.Type, "sequence", res.Sequence,
		"schedules", res.Schedules, "deletes", res.Deletes)
	return nil
}

func (im *Importer) sequence(ctx context.Context, name string) (int, error) {
	r, err := im.open(ctx, name)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	rec, err := schedule.NewReader(r).Next()
	if err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}
	if rec.Header == nil {
		return 0, errors.New("no JsonTimetableV1 header")
	}
	return rec.Header.Sequence, nil
}

func currentSequence(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	v, err := db.GetState(ctx, pool, "schedule_sequence")
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.Atoi(v)
}

// open opens a file, decompressing .gz files.
func (im *Importer) open(ctx context.Context, name string) (io.ReadCloser, error) {
	r, err := im.Source.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(name), ".gz") {
		return r, nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, closers{gz, r}}, nil
}

type closers []io.Closer

func (c closers) Close() error {
	var first error
	for _, x := range c {
		if err := x.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (im *Importer) seen(ctx context.Context, o Object) (bool, error) {
	var n int
	err := im.Pool.QueryRow(ctx, `SELECT count(*) FROM inbox_files WHERE name = $1 AND size = $2 AND modified = $3`,
		o.Name, o.Size, o.Modified).Scan(&n)
	return n > 0, err
}

// record notes that a file has been dealt with. Failed loads are recorded
// too, so a bad file is not retried forever; re-delivering it (a new
// modification time) retries it.
func (im *Importer) record(ctx context.Context, f file, err error) {
	result := "loaded"
	if err != nil {
		result = err.Error()
		if !strings.HasPrefix(result, "skipped") {
			slog.Warn("inbox: file failed", "file", f.Name, "err", err)
		}
	}
	if _, dbErr := im.Pool.Exec(ctx, `INSERT INTO inbox_files (name, size, modified, kind, result)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name, size, modified) DO UPDATE SET result = EXCLUDED.result,
		processed_at = now()`, f.Name, f.Size, f.Modified, f.kind, result); dbErr != nil {
		slog.Warn("inbox: could not record file", "file", f.Name, "err", dbErr)
	}
}
