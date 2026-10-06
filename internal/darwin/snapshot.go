package darwin

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// SnapshotResult counts what a snapshot contained.
type SnapshotResult struct {
	TrainStatus  int
	Schedules    int
	Associations int
	Messages     int
}

// ApplySnapshot streams a Darwin snapshot (one or more Push Port documents,
// typically a single sR holding every current train) and applies each
// element as it is read, so the whole file never sits in memory. A snapshot
// restores state after a restart: Darwin otherwise only sends changes.
func (a *Applier) ApplySnapshot(ctx context.Context, r io.Reader) (*SnapshotResult, error) {
	dec := xml.NewDecoder(r)
	res := &SnapshotResult{}
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("snapshot: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var applyErr error
		switch start.Name.Local {
		case "TS":
			var ts TrainStatus
			if err := dec.DecodeElement(&ts, &start); err != nil {
				return res, fmt.Errorf("snapshot TS: %w", err)
			}
			res.TrainStatus++
			applyErr = a.applyTS(ctx, &ts)
		case "schedule":
			var s Schedule
			if err := dec.DecodeElement(&s, &start); err != nil {
				return res, fmt.Errorf("snapshot schedule: %w", err)
			}
			res.Schedules++
			applyErr = a.applySchedule(ctx, &s)
		case "association":
			var as Association
			if err := dec.DecodeElement(&as, &start); err != nil {
				return res, fmt.Errorf("snapshot association: %w", err)
			}
			res.Associations++
			applyErr = a.applyAssociation(ctx, &as)
		case "OW":
			var m StationMessage
			if err := dec.DecodeElement(&m, &start); err != nil {
				return res, fmt.Errorf("snapshot OW: %w", err)
			}
			res.Messages++
			applyErr = a.applyMessage(ctx, &m)
		default:
			continue
		}
		if applyErr != nil && !errors.Is(applyErr, errNoService) {
			slog.Debug("snapshot element failed", "element", start.Name.Local, "err", applyErr)
		}
	}
}
