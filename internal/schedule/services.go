package schedule

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// ServiceWindowDays is how many days ahead of today services are resolved.
// Yesterday is always included so trains running past midnight stay visible.
const ServiceWindowDays = 7

// resolveSQL picks, for every train UID and run date in [$1, $2], the
// schedule that applies. The rule is the CIF one: among schedules valid on
// the date and running on its weekday, the lowest STP indicator wins
// (C < N < O < P), with a VSTP schedule beating a CIF one on a tie since it
// is the more recent. A winning C (STP cancellation) keeps the schedule it
// cancels so the cancelled train can still be shown.
//
// Bank holiday running restrictions are not applied yet.
const resolveSQL = `
WITH days AS (
    SELECT d::date AS run_date FROM generate_series($1::date, $2::date, '1 day') d
),
valid AS (
    SELECT s.id, s.train_uid, s.stp, s.source, days.run_date
    FROM days
    JOIN schedules s ON days.run_date BETWEEN s.start_date AND s.end_date
     AND substr(s.days_runs, extract(isodow FROM days.run_date)::int, 1) = '1'
    WHERE $3::text IS NULL OR s.train_uid = $3
),
winner AS (
    SELECT DISTINCT ON (train_uid, run_date) train_uid, run_date, id, stp
    FROM valid ORDER BY train_uid, run_date, stp, source DESC
),
base AS (
    SELECT DISTINCT ON (train_uid, run_date) train_uid, run_date, id
    FROM valid WHERE stp <> 'C' ORDER BY train_uid, run_date, stp, source DESC
),
resolved AS (
    SELECT w.train_uid, w.run_date,
           CASE WHEN w.stp = 'C' THEN b.id ELSE w.id END AS schedule_id,
           w.stp = 'C' AS planned_cancel
    FROM winner w LEFT JOIN base b USING (train_uid, run_date)
    WHERE w.stp <> 'C' OR b.id IS NOT NULL
),
upserted AS (
    INSERT INTO services (train_uid, run_date, schedule_id, planned_cancel)
    SELECT train_uid, run_date, schedule_id, planned_cancel FROM resolved
    ON CONFLICT (train_uid, run_date) DO UPDATE
       SET schedule_id = EXCLUDED.schedule_id, planned_cancel = EXCLUDED.planned_cancel
     WHERE services.schedule_id IS DISTINCT FROM EXCLUDED.schedule_id
        OR services.planned_cancel IS DISTINCT FROM EXCLUDED.planned_cancel
    RETURNING 1
)
DELETE FROM services sv
WHERE sv.run_date BETWEEN $1 AND $2
  AND ($3::text IS NULL OR sv.train_uid = $3)
  AND sv.trust_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM resolved r
                  WHERE r.train_uid = sv.train_uid AND r.run_date = sv.run_date)`

// RefreshServices resolves services from yesterday to ServiceWindowDays ahead.
func RefreshServices(ctx context.Context, pool *pgxpool.Pool) error {
	today := ukrail.Today()
	return RefreshRange(ctx, pool, today.AddDate(0, 0, -1), today.AddDate(0, 0, ServiceWindowDays))
}

// RefreshRange resolves services for every run date in [from, to], then
// refreshes planner statistics, which bulk loads otherwise leave stale.
func RefreshRange(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) error {
	if err := refresh(ctx, pool, from, to, nil); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `ANALYZE schedules, schedule_locations, services`)
	return err
}

func refresh(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, uid *string) error {
	_, err := pool.Exec(ctx, resolveSQL, from, to, uid)
	return err
}

// RefreshTrain resolves one train UID for one run date.
func RefreshTrain(ctx context.Context, pool *pgxpool.Pool, uid string, runDate time.Time) error {
	return refresh(ctx, pool, runDate, runDate, &uid)
}
