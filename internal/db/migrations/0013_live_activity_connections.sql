-- The previous leg of a Live Activity's journey, so the connection time can
-- be worked out from both trains' live times instead of fixed at
-- registration. Changes to either train update the activity.
ALTER TABLE live_activities
    ADD COLUMN previous_train_uid   text,
    ADD COLUMN previous_run_date    date,
    -- Where the previous train is left: this leg's origin unless the
    -- connection involves a change of station.
    ADD COLUMN previous_arrival_crs text;
CREATE INDEX live_activities_previous_idx ON live_activities (previous_train_uid, previous_run_date)
    WHERE previous_train_uid IS NOT NULL;
