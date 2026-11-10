ALTER TABLE findings ADD COLUMN reanalysis_origin INTEGER NOT NULL DEFAULT 0 CHECK (reanalysis_origin IN (0, 1));
ALTER TABLE findings ADD COLUMN processing_revision INTEGER NOT NULL DEFAULT 1 CHECK (processing_revision > 0);
UPDATE findings SET reanalysis_origin = 1 WHERE reanalyze_requested_at IS NOT NULL;

ALTER TABLE seller_profiles ADD COLUMN seller_good_count INTEGER CHECK (seller_good_count >= 0);
ALTER TABLE seller_profiles ADD COLUMN seller_rate_count INTEGER CHECK (seller_rate_count >= 0);
ALTER TABLE seller_profiles ADD COLUMN buyer_good_count INTEGER CHECK (buyer_good_count >= 0);
ALTER TABLE seller_profiles ADD COLUMN buyer_rate_count INTEGER CHECK (buyer_rate_count >= 0);

CREATE TABLE run_skip_events (
    run_id INTEGER NOT NULL REFERENCES run_records (id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK (seq > 0),
    at INTEGER NOT NULL,
    PRIMARY KEY (run_id, seq)
);
CREATE INDEX run_skip_events_at ON run_skip_events (at);

CREATE TABLE notification_intents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id INTEGER NOT NULL,
    task_id INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('recommended', 'new', 'price_drop', 'price_upgrade')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'abandoned', 'cancelled')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    price INTEGER CHECK (price >= 0),
    previous_price INTEGER CHECK (previous_price >= 0),
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    first_failed_at INTEGER,
    sent_at INTEGER,
    reason TEXT
);
CREATE UNIQUE INDEX notification_intents_one_pending ON notification_intents (finding_id) WHERE state = 'pending';
CREATE INDEX notification_intents_state_created_at ON notification_intents (state, created_at);
CREATE INDEX notification_intents_task_id_state ON notification_intents (task_id, state);
CREATE INDEX notification_intents_finding_id_created_at ON notification_intents (finding_id, created_at);
CREATE INDEX notification_intents_sent_at ON notification_intents (sent_at);

CREATE TABLE delivery_intents (
    delivery_id INTEGER NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    intent_id INTEGER NOT NULL REFERENCES notification_intents (id) ON DELETE CASCADE,
    PRIMARY KEY (delivery_id, intent_id)
);
CREATE INDEX delivery_intents_intent_id ON delivery_intents (intent_id);

CREATE TEMP TABLE migration_business_now (at INTEGER NOT NULL);
INSERT INTO migration_business_now(at)
SELECT CAST(MAX(
    unixepoch('subsec') * 1000,
    COALESCE((SELECT CASE WHEN json_valid(value) THEN json_extract(value, '$.highest_utc') END FROM meta WHERE key = 'quota.clock'), 0)
) AS INTEGER);

-- 旧计数仍保留；没有可证失败时间时停止重发，不凭空开启新窗口。
INSERT INTO notification_intents (
    finding_id, task_id, kind, state, created_at, updated_at, price,
    failure_count, first_failed_at, sent_at, reason
)
SELECT f.id, f.task_id,
    COALESCE(f.notify_kind, CASE h.only_kind WHEN 'all_new' THEN 'new' ELSE 'recommended' END),
    CASE
        WHEN f.notify_kind IS NULL THEN 'cancelled'
        WHEN f.notified_at IS NOT NULL THEN 'sent'
        WHEN f.notify_skip_reason IS NOT NULL THEN 'cancelled'
        WHEN f.notify_failures > 0 AND h.first_failed_at IS NULL THEN 'abandoned'
        ELSE 'pending'
    END,
    COALESCE(h.first_at, f.analyzed_at, f.found_at),
    COALESCE(f.notified_at, h.last_at, f.analyzed_at, f.found_at),
    f.recommended_price,
    f.notify_failures, h.first_failed_at, f.notified_at,
    CASE
        WHEN (f.notify_kind IS NULL OR f.notified_at IS NULL) AND f.notify_skip_reason IS NOT NULL THEN f.notify_skip_reason
        WHEN f.notify_kind IS NULL THEN '{"code":"notification.legacy_retry_unknown","args":{}}'
        WHEN f.notified_at IS NULL AND f.notify_failures > 0 AND h.first_failed_at IS NULL
            THEN '{"code":"notification.legacy_retry_unknown","args":{}}'
    END
FROM findings f
LEFT JOIN (
    SELECT di.finding_id,
        MIN(CASE WHEN d.kind IN ('recommended', 'all_new') THEN d.at END) AS first_at,
        MAX(CASE WHEN d.kind IN ('recommended', 'all_new') THEN d.at END) AS last_at,
        MIN(CASE WHEN d.ok = 0 AND d.kind IN ('recommended', 'all_new') THEN d.at END) AS first_failed_at,
        COUNT(DISTINCT d.kind) AS kind_count, MIN(d.kind) AS only_kind,
        SUM(CASE WHEN d.kind IS NULL OR d.kind NOT IN ('recommended', 'all_new') THEN 1 ELSE 0 END) AS other_count
    FROM delivery_items di JOIN deliveries d ON d.id = di.delivery_id
    GROUP BY di.finding_id
) h ON h.finding_id = f.id
WHERE f.notify_kind IS NOT NULL
    OR (f.notify_failures > 0 AND h.kind_count = 1 AND h.other_count = 0);

-- 已取消且种类不可证的旧计数只归档，不猜提醒种类，也不再参与重试。
INSERT INTO meta (key, value)
SELECT 'migration.legacy_notify_failures.' || f.id, CAST(f.notify_failures AS TEXT)
FROM findings f
WHERE f.notify_kind IS NULL AND f.notify_failures <> 0
    AND NOT EXISTS (SELECT 1 FROM notification_intents ni WHERE ni.finding_id = f.id);

INSERT INTO delivery_intents (delivery_id, intent_id)
SELECT d.id, ni.id
FROM notification_intents ni
JOIN findings f ON f.id = ni.finding_id
JOIN delivery_items di ON di.finding_id = f.id
JOIN deliveries d ON d.id = di.delivery_id
WHERE d.kind = CASE ni.kind WHEN 'new' THEN 'all_new' ELSE 'recommended' END
    AND (ni.sent_at IS NULL OR d.at <= ni.sent_at);

UPDATE findings
SET notify_skip_reason = '{"code":"notification.legacy_retry_unknown","args":{}}'
WHERE id IN (SELECT finding_id FROM notification_intents WHERE state = 'abandoned');

INSERT INTO run_skip_events (run_id, seq, at)
SELECT id, 1, COALESCE(ended_at, started_at)
FROM run_records
WHERE outcome = 'skipped' AND repeat_count = 1
    AND COALESCE(ended_at, started_at) IS NOT NULL;

-- 旧降价和折叠跳过缺少逐次事实，保留原历史并标明统计覆盖起点。
INSERT INTO meta (key, value)
SELECT 'stats.coverage.notification_intents', CAST(at AS TEXT) FROM migration_business_now
WHERE EXISTS (SELECT 1 FROM findings WHERE notify_kind IS NOT NULL OR notify_failures <> 0)
    OR EXISTS (SELECT 1 FROM deliveries WHERE kind IN ('price_drop', 'summary'))
ON CONFLICT(key) DO UPDATE SET value = CAST(MAX(CAST(meta.value AS INTEGER), CAST(excluded.value AS INTEGER)) AS TEXT);
INSERT INTO meta (key, value)
SELECT 'stats.coverage.run_skip_events', CAST(at AS TEXT) FROM migration_business_now
WHERE EXISTS (SELECT 1 FROM run_records WHERE outcome = 'skipped' AND (repeat_count <> 1 OR COALESCE(ended_at, started_at) IS NULL))
ON CONFLICT(key) DO UPDATE SET value = CAST(MAX(CAST(meta.value AS INTEGER), CAST(excluded.value AS INTEGER)) AS TEXT);
DROP TABLE migration_business_now;

ALTER TABLE findings DROP COLUMN notify_failures;
