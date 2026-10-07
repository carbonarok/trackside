-- iOS Live Activities following one leg of a journey, kept up to date by
-- APNs pushes while the app is in the background. A push token belongs to one
-- activity and can change during its life; the app re-registers when it does.
CREATE TABLE live_activities (
    activity_id        text PRIMARY KEY,
    push_token         text NOT NULL,
    bundle_id          text NOT NULL,
    train_uid          text NOT NULL,
    run_date           date NOT NULL,
    origin_crs         text NOT NULL,
    destination_crs    text NOT NULL,
    -- Minutes to make this train from the previous leg, if the app knows.
    connection_minutes integer,
    -- The APNs host its token was last accepted by, once known.
    apns_host          text,
    -- The content-state last sent, to skip unchanged pushes and spot changes
    -- worth an alert; and its aps timestamp, which must keep increasing.
    last_state         jsonb,
    last_timestamp     bigint NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    -- Last registration or delivered push; idle activities are removed.
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX live_activities_service_idx ON live_activities (train_uid, run_date);
