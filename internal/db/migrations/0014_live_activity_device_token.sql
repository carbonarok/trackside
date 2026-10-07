-- The device's own APNs token, for a normal notification (with its text in
-- Notification Center) when a Live Activity changes in a way worth an alert.
-- A Live Activity's token can't carry one: iOS shows those alerts on the
-- activity, not as notifications.
ALTER TABLE live_activities ADD COLUMN device_token text;
