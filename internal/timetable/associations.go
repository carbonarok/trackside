package timetable

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Association links a service to another train at a location.
type Association struct {
	// Type describes the link from this service's point of view: divides,
	// divided_from, joined_by, joins, forms or formed_from.
	Type      string
	Category  string // JJ, VV, NP or LK
	Location  Location
	Cancelled bool
	// Other is the associated train, if it is in the timetable.
	OtherUID     string
	OtherRunDate time.Time
	Other        *Service
}

var associationTypes = map[string][2]string{
	// category: {as main train, as associated train}
	"VV": {"divides", "divided_from"},
	"JJ": {"joined_by", "joins"},
	"NP": {"forms", "formed_from"},
	"LK": {"linked", "linked"},
}

// assocSQL resolves timetable associations for one train on one run date,
// from both sides. Dates and days apply to the main train, so when this
// train is the associated one its date is shifted back by date_indicator
// first. Like schedules, the lowest STP indicator wins, and a winning C
// removes the association.
const assocSQL = `
WITH as_main AS (
    SELECT DISTINCT ON (a.assoc_uid, a.tiploc) a.assoc_uid AS other_uid, a.tiploc,
           COALESCE(a.category, '') AS category, a.stp, true AS is_main,
           $2::date + CASE a.date_indicator WHEN 'N' THEN 1 WHEN 'P' THEN -1 ELSE 0 END AS other_date
    FROM associations a
    WHERE a.main_uid = $1
      AND $2::date BETWEEN a.start_date AND a.end_date
      AND substr(a.days_runs, extract(isodow FROM $2::date)::int, 1) = '1'
    ORDER BY a.assoc_uid, a.tiploc, a.stp
),
as_assoc AS (
    SELECT DISTINCT ON (a.main_uid, a.tiploc) a.main_uid AS other_uid, a.tiploc,
           COALESCE(a.category, '') AS category, a.stp, false AS is_main, m.main_date AS other_date
    FROM associations a
    CROSS JOIN LATERAL (SELECT $2::date - CASE a.date_indicator WHEN 'N' THEN 1 WHEN 'P' THEN -1 ELSE 0 END
                        AS main_date) m
    WHERE a.assoc_uid = $1
      AND m.main_date BETWEEN a.start_date AND a.end_date
      AND substr(a.days_runs, extract(isodow FROM m.main_date)::int, 1) = '1'
    ORDER BY a.main_uid, a.tiploc, a.stp
)
SELECT other_uid, tiploc, category, is_main, other_date FROM as_main WHERE stp <> 'C'
UNION ALL
SELECT other_uid, tiploc, category, is_main, other_date FROM as_assoc WHERE stp <> 'C'`

// darwinAssocSQL finds Darwin's live associations for a service, with the
// other train resolved through its RID.
const darwinAssocSQL = `
SELECT d.tiploc, d.category, d.cancelled, d.main_rid = $1 AS is_main, o.train_uid, o.run_date
FROM darwin_associations d
JOIN services o ON o.darwin_rid = CASE WHEN d.main_rid = $1 THEN d.assoc_rid ELSE d.main_rid END
WHERE d.main_rid = $1 OR d.assoc_rid = $1`

// Associations returns the trains this service joins, divides from or
// forms, merging Darwin's live changes over the timetable.
func (st *Store) Associations(ctx context.Context, svc *Service) ([]Association, error) {
	type key struct {
		uid, tiploc, category string
	}
	byKey := map[key]*Association{}
	var order []key
	add := func(uid, tiploc, category string, isMain bool, date time.Time, cancelled bool) {
		k := key{uid, tiploc, category}
		if a := byKey[k]; a != nil {
			a.Cancelled = cancelled
			return
		}
		role := 1
		if isMain {
			role = 0
		}
		typ := associationTypes[category][role]
		if typ == "" {
			typ = "associated"
		}
		byKey[k] = &Association{Type: typ, Category: category, Cancelled: cancelled,
			Location: Location{TIPLOC: tiploc}, OtherUID: uid, OtherRunDate: date}
		order = append(order, k)
	}

	rows, err := st.Pool.Query(ctx, assocSQL, svc.UID, svc.RunDate)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var uid, tiploc, category string
		var isMain bool
		var date time.Time
		if err := rows.Scan(&uid, &tiploc, &category, &isMain, &date); err != nil {
			return nil, err
		}
		add(uid, tiploc, category, isMain, date, false)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if svc.DarwinRID != "" {
		rows, err := st.Pool.Query(ctx, darwinAssocSQL, svc.DarwinRID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, tiploc, category string
			var cancelled, isMain bool
			var date time.Time
			if err := rows.Scan(&tiploc, &category, &cancelled, &isMain, &uid, &date); err != nil {
				return nil, err
			}
			add(uid, tiploc, category, isMain, date, cancelled)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(order) == 0 {
		return nil, nil
	}

	// Load the other trains and name the locations.
	ids := map[key]int64{}
	var idList []int64
	for _, k := range order {
		a := byKey[k]
		var id int64
		err := st.Pool.QueryRow(ctx, `SELECT id FROM services WHERE train_uid = $1 AND run_date = $2`,
			a.OtherUID, a.OtherRunDate).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ids[k] = id
		idList = append(idList, id)
	}
	others, err := st.load(ctx, idList)
	if err != nil {
		return nil, err
	}
	out := make([]Association, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		a.Other = others[ids[k]]
		for i := range svc.Stops {
			if svc.Stops[i].Location.TIPLOC == a.Location.TIPLOC {
				a.Location = svc.Stops[i].Location
				break
			}
		}
		out = append(out, *a)
	}
	// Order by where on this service's route the association happens.
	pos := func(tiploc string) int {
		for i := range svc.Stops {
			if svc.Stops[i].Location.TIPLOC == tiploc {
				return i
			}
		}
		return len(svc.Stops)
	}
	sort.SliceStable(out, func(i, j int) bool { return pos(out[i].Location.TIPLOC) < pos(out[j].Location.TIPLOC) })
	return out, nil
}
