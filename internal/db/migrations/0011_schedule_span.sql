-- First and last working times of each schedule (seconds after midnight of
-- the run date), so trains running at a given moment can be found without
-- scanning every schedule location.
ALTER TABLE schedules
    ADD COLUMN first_time integer,
    ADD COLUMN last_time  integer;

UPDATE schedules s SET first_time = b.f, last_time = b.l
FROM (SELECT schedule_id,
             min(COALESCE(wtt_dep, wtt_pass, wtt_arr)) AS f,
             max(COALESCE(wtt_arr, wtt_pass, wtt_dep)) AS l
      FROM schedule_locations GROUP BY schedule_id) b
WHERE b.schedule_id = s.id;
