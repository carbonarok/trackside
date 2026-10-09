# How it works

```
 SCHEDULE (daily) ──┐
 VSTP (stream) ─────┼─► schedules + schedule_locations ─► services (per run date) ─► API
 CORPUS ────────────┘                                        ▲
 TRUST (stream) ──────────┐                                  │
 TD + SMART (stream) ─────┴──► service_events ───────────────┤
 Darwin (stream) ─────────────► darwin_forecasts ────────────┘
```

- **Schedules** are stored as published. Times are kept as seconds after
  midnight of the run date, so a train that crosses midnight just has values
  of 86400 or more.
- **Services** are worked out for yesterday through 7 days ahead. For each
  train UID and date, the lowest STP indicator wins (C < N < O < P), and a
  VSTP schedule beats a CIF one on a tie. A winning cancellation keeps the
  schedule it cancels, so the train can be shown as cancelled.
- **Bank holidays need no special handling.** `CIF_bank_holiday_running` only
  adds the "BHX" symbol to printed timetables. The real bank holiday changes
  are published as STP overlays and cancellations, which the rule above
  already applies. trackside deliberately ignores the flag, because acting on
  it would remove trains that do run.
- **TRUST** movements are matched to a stop by STANOX and planned time.
  Most TRUST timestamps are UK local time encoded as if it were UTC, and are
  decoded that way. Activations take their run date from
  `origin_dep_timestamp`, because `tp_origin_timestamp` is wrong for trains
  starting just after midnight in BST.
- **Precedence** for actual times: TRUST, then Darwin, then TD. For estimates,
  Darwin's forecast beats trackside's projection. Darwin Lite data is treated
  as Darwin, but only fills stops the Darwin stream hasn't covered.
- **Live data is keyed by stop position.** It also records the TIPLOC, so if a
  schedule changes while the train runs, the data is re-attached to the right
  stop.
- **Restarts:** trains that TRUST activated before trackside started are
  picked up from their first movement. The headcode and start day in the
  TRUST train ID, plus the location and planned time, identify the service.
- **TD steps** are matched to a service by headcode, the SMART location and
  the nearest working time, preferring trains TRUST has activated.
- **Approaching** means the train has stepped into the berth that a SMART
  arrival step starts from. It clears when the arrival is reported, or after
  20 minutes without one.
