package timetable

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Message is a Darwin station message.
type Message struct {
	ID        int
	Category  string
	Severity  int
	Text      string
	HTML      string
	UpdatedAt time.Time
}

// Messages returns the station messages for a CRS, most severe first.
// Messages Darwin marks as suppressed are left out.
func (st *Store) Messages(ctx context.Context, crs string) ([]Message, error) {
	if crs == "" {
		return nil, nil
	}
	rows, err := st.Pool.Query(ctx, `SELECT id, category, severity, text, html, updated_at
		FROM station_messages WHERE $1 = ANY(stations) AND NOT suppress
		ORDER BY severity DESC, updated_at DESC`, crs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		err := r.Scan(&m.ID, &m.Category, &m.Severity, &m.Text, &m.HTML, &m.UpdatedAt)
		return m, err
	})
}
