ALTER TABLE system_events ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0);
ALTER TABLE system_events ADD COLUMN first_failed_at INTEGER;
ALTER TABLE deliveries ADD COLUMN event_id INTEGER;
CREATE INDEX deliveries_event_id_at ON deliveries (event_id, at);
