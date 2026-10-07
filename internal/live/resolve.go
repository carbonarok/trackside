package live

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// NewResolver resolves changes from the database: each changed service
// touches its own train page and the board at every place it calls or
// passes, since a delay carries forward to every later stop.
func NewResolver(pool *pgxpool.Pool) Resolver {
	return func(ctx context.Context, c Changes) ([]string, error) {
		today := ukrail.DateOf(time.Now())
		rows, err := pool.Query(ctx, `
			SELECT sv.train_uid, sv.run_date,
			       COALESCE(array_agg(DISTINCT sl.tiploc) FILTER (WHERE sl.tiploc IS NOT NULL), '{}'),
			       COALESCE(array_agg(DISTINCT l.crs) FILTER (WHERE l.crs IS NOT NULL), '{}')
			FROM services sv
			LEFT JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
			LEFT JOIN locations l ON l.tiploc = sl.tiploc
			WHERE sv.id = ANY($1)
			   OR (sv.train_uid = ANY($2) AND sv.run_date BETWEEN $4 AND $5)
			   OR sv.darwin_rid = ANY($3)
			GROUP BY sv.id, sv.train_uid, sv.run_date`,
			c.Services, c.UIDs, c.RIDs, today.AddDate(0, 0, -1), today.AddDate(0, 0, 1))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		seen := map[string]struct{}{}
		var topics []string
		add := func(t string) {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				topics = append(topics, t)
			}
		}
		for rows.Next() {
			var uid string
			var runDate time.Time
			var tiplocs, crs []string
			if err := rows.Scan(&uid, &runDate, &tiplocs, &crs); err != nil {
				return nil, err
			}
			add("train:" + uid + "|" + runDate.Format(time.DateOnly))
			for _, s := range append(tiplocs, crs...) {
				add("station:" + s)
			}
		}
		return topics, rows.Err()
	}
}
