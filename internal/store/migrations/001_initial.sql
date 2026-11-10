CREATE TABLE admin (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    totp_secret_enc BLOB,
    totp_enabled_at INTEGER,
    totp_last_step INTEGER,
    created_at INTEGER NOT NULL,
    password_changed_at INTEGER NOT NULL
);

CREATE TABLE auth_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('web_session', 'api_token')),
    token_hash BLOB NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at INTEGER,
    last_used_at INTEGER,
    expires_at INTEGER,
    revoked_at INTEGER
);
CREATE INDEX auth_tokens_kind_revoked_at ON auth_tokens (kind, revoked_at);

CREATE TABLE settings (
    key TEXT NOT NULL PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at INTEGER
);

CREATE TABLE prompts (
    key TEXT NOT NULL PRIMARY KEY CHECK (key IN ('analysis', 'title_screen', 'profile', 'market')),
    content TEXT NOT NULL,
    updated_at INTEGER
);

CREATE TABLE proxies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    url_enc BLOB NOT NULL,
    created_at INTEGER
);

CREATE TABLE accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    proxy_id INTEGER REFERENCES proxies (id),
    nickname TEXT,
    avatar_url TEXT,
    state TEXT NOT NULL CHECK (state IN ('signed_out', 'active', 'cooling', 'needs_verification', 'denied', 'expired')),
    state_reason TEXT,
    state_changed_at INTEGER,
    cooldown_until INTEGER,
    verify_level INTEGER NOT NULL DEFAULT 0,
    verify_url TEXT,
    last_cooldown_end_at INTEGER,
    cooldown_manual_used INTEGER NOT NULL DEFAULT 0 CHECK (cooldown_manual_used IN (0, 1)),
    last_run_ended_at INTEGER,
    next_gap_seconds INTEGER,
    last_used_at INTEGER,
    created_at INTEGER
);

CREATE TABLE quota_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('search', 'detail')),
    run_id INTEGER,
    at INTEGER NOT NULL
);
CREATE INDEX quota_events_account_id_at ON quota_events (account_id, at);

CREATE TABLE tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 30),
    keyword TEXT NOT NULL CHECK (length(keyword) BETWEEN 1 AND 50),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    account_id INTEGER REFERENCES accounts (id) ON DELETE SET NULL,
    account_pinned INTEGER NOT NULL DEFAULT 0 CHECK (account_pinned IN (0, 1)),
    min_price INTEGER CHECK (min_price >= 0),
    max_price INTEGER CHECK (max_price >= 0),
    personal_only INTEGER NOT NULL DEFAULT 1 CHECK (personal_only IN (0, 1)),
    free_shipping_only INTEGER NOT NULL DEFAULT 0 CHECK (free_shipping_only IN (0, 1)),
    publish_window TEXT NOT NULL DEFAULT 'any' CHECK (publish_window IN ('any', 'day1', 'day3', 'day7', 'day14')),
    region_province TEXT,
    region_city TEXT,
    region_district TEXT,
    max_pages INTEGER NOT NULL CHECK (max_pages >= 1),
    interval_minutes INTEGER NOT NULL CHECK (interval_minutes >= 1),
    crawl_overrides_json TEXT NOT NULL DEFAULT '{}',
    judge_mode TEXT NOT NULL CHECK (judge_mode IN ('ai', 'keyword')),
    requirement TEXT NOT NULL DEFAULT '',
    profile_json TEXT NOT NULL DEFAULT '{}',
    budget_min INTEGER,
    budget_max INTEGER,
    match_keywords_json TEXT NOT NULL DEFAULT '[]',
    exclude_keywords_json TEXT NOT NULL DEFAULT '[]',
    analyze_on_first_run INTEGER NOT NULL DEFAULT 1 CHECK (analyze_on_first_run IN (0, 1)),
    notify_policy TEXT NOT NULL CHECK (notify_policy IN ('steal', 'good', 'fair', 'all_new', 'none', 'recommended')),
    notify_price_drop INTEGER NOT NULL DEFAULT 1 CHECK (notify_price_drop IN (0, 1)),
    search_generation INTEGER NOT NULL DEFAULT 1,
    baseline_pending INTEGER NOT NULL DEFAULT 1 CHECK (baseline_pending IN (0, 1)),
    sort_index INTEGER NOT NULL,
    created_at INTEGER,
    updated_at INTEGER,
    last_run_at INTEGER,
    last_success_at INTEGER,
    next_run_at INTEGER,
    last_run_outcome TEXT CHECK (last_run_outcome IN ('success', 'failed', 'skipped')),
    last_error TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    failure_paused_until INTEGER,
    no_good_hint_dismissed_at INTEGER,
    CHECK (min_price <= max_price)
);
CREATE INDEX tasks_enabled_next_run_at ON tasks (enabled, next_run_at);
CREATE INDEX tasks_account_id ON tasks (account_id);

CREATE TABLE channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL CHECK (type IN ('bark', 'telegram', 'ntfy', 'gotify', 'wecom', 'dingtalk', 'feishu', 'pushplus', 'serverchan', 'webhook', 'email')),
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    config_json TEXT NOT NULL,
    secret_enc BLOB,
    last_test_at INTEGER,
    last_test_ok INTEGER CHECK (last_test_ok IN (0, 1)),
    last_error TEXT,
    UNIQUE (type, name)
);

CREATE TABLE task_channels (
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, channel_id)
);

CREATE TABLE listings (
    item_id TEXT NOT NULL PRIMARY KEY,
    title TEXT NOT NULL,
    price INTEGER,
    price_text TEXT,
    original_price_text TEXT,
    thumbnail_url TEXT,
    image_urls_json TEXT,
    desktop_url TEXT NOT NULL,
    area TEXT,
    seller_id TEXT,
    seller_nick TEXT,
    published_at INTEGER,
    want_count INTEGER,
    browse_count INTEGER,
    tags_json TEXT,
    description TEXT,
    seller_registration_days INTEGER,
    zhima_level TEXT,
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    detail_fetched_at INTEGER,
    is_favorite INTEGER NOT NULL DEFAULT 0 CHECK (is_favorite IN (0, 1)),
    favorited_at INTEGER,
    is_dismissed INTEGER NOT NULL DEFAULT 0 CHECK (is_dismissed IN (0, 1)),
    last_drop_amount INTEGER,
    last_drop_at INTEGER,
    sale_status TEXT CHECK (sale_status IN ('on_sale', 'sold', 'offline'))
);
CREATE INDEX listings_last_seen_at ON listings (last_seen_at);
CREATE INDEX listings_seller_id ON listings (seller_id);
CREATE INDEX listings_is_favorite ON listings (is_favorite);

CREATE TABLE price_points (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id TEXT REFERENCES listings (item_id) ON DELETE CASCADE,
    price INTEGER NOT NULL,
    observed_at INTEGER NOT NULL
);
CREATE INDEX price_points_item_id_observed_at ON price_points (item_id, observed_at);

CREATE TABLE task_sightings (
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    item_id TEXT NOT NULL,
    day TEXT NOT NULL,
    price INTEGER,
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, day, item_id)
);

CREATE TABLE task_item_specs (
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    item_id TEXT NOT NULL,
    kind TEXT CHECK (kind IN ('sale', 'wanted', 'accessory', 'recycle', 'rental', 'repair', 'box', 'multi', 'broken', 'other')),
    specs_json TEXT,
    brand_new INTEGER CHECK (brand_new IN (0, 1)),
    config_key TEXT,
    excluded INTEGER NOT NULL DEFAULT 0 CHECK (excluded IN (0, 1)),
    source TEXT NOT NULL CHECK (source IN ('title', 'detail', 'user')),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, item_id)
);

CREATE TABLE seller_profiles (
    seller_id TEXT NOT NULL PRIMARY KEY,
    nick TEXT,
    avatar_url TEXT,
    introduction TEXT,
    seller_credit TEXT,
    buyer_credit TEXT,
    seller_credit_level TEXT,
    buyer_credit_level TEXT,
    item_count INTEGER,
    rate_count INTEGER,
    on_sale_count INTEGER,
    sold_count INTEGER,
    seller_good_rate REAL,
    buyer_good_rate REAL,
    recent_items_json TEXT,
    recent_rates_json TEXT,
    fetched_at INTEGER NOT NULL
);

CREATE TABLE blocked_sellers (
    seller_id TEXT NOT NULL PRIMARY KEY,
    nick TEXT,
    created_at INTEGER
);

CREATE TABLE seen_items (
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    item_id TEXT NOT NULL,
    first_seen_at INTEGER,
    PRIMARY KEY (task_id, item_id)
);

CREATE TABLE findings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    item_id TEXT NOT NULL REFERENCES listings (item_id),
    search_generation INTEGER NOT NULL,
    found_at INTEGER NOT NULL,
    is_baseline INTEGER NOT NULL CHECK (is_baseline IN (0, 1)),
    decision TEXT NOT NULL CHECK (decision IN ('pending', 'scored', 'matched', 'rejected', 'failed', 'skipped')),
    reject_kind TEXT CHECK (reject_kind IN ('filter', 'excluded', 'junk', 'unmatched', 'keyword_miss')),
    stage TEXT NOT NULL CHECK (stage IN ('title', 'detail', 'done')),
    decided_layer INTEGER CHECK (decided_layer IN (1, 2, 3)),
    reason TEXT,
    analysis_json TEXT,
    user_overrides_json TEXT,
    score INTEGER CHECK (score BETWEEN 0 AND 100),
    grade TEXT CHECK (grade IN ('steal', 'good', 'fair', 'pricey', 'avoid')),
    score_breakdown_json TEXT,
    fair_price INTEGER,
    fair_sample INTEGER,
    fair_basis TEXT,
    scored_price INTEGER,
    scored_at INTEGER,
    summary TEXT,
    seller_type TEXT CHECK (seller_type IN ('personal', 'dealer_suspect', 'dealer', 'unknown')),
    title_screen_reason TEXT,
    matched_keywords_json TEXT,
    analysis_model TEXT,
    analysis_provider_id INTEGER,
    analyzed_at INTEGER,
    attempts INTEGER NOT NULL DEFAULT 0,
    retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1)),
    reanalyze_requested_at INTEGER,
    is_read INTEGER NOT NULL CHECK (is_read IN (0, 1)),
    notify_kind TEXT CHECK (notify_kind IN ('recommended', 'new')),
    notify_skip_reason TEXT,
    notified_at INTEGER,
    notify_failures INTEGER NOT NULL DEFAULT 0,
    recommended_price INTEGER,
    last_drop_notified_price INTEGER,
    drop_notified_at INTEGER,
    UNIQUE (task_id, item_id)
);
CREATE INDEX findings_task_id_found_at ON findings (task_id, found_at);
CREATE INDEX findings_task_id_decision ON findings (task_id, decision);
CREATE INDEX findings_task_id_score ON findings (task_id, score);
CREATE INDEX findings_is_read ON findings (is_read);
CREATE INDEX findings_item_id ON findings (item_id);

CREATE TABLE ai_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    preset TEXT CHECK (preset IN ('openai', 'deepseek', 'modelscope', 'siliconflow', 'openrouter', 'aliyun', 'anthropic', 'ollama', 'custom')),
    base_url TEXT NOT NULL,
    api_key_enc BLOB,
    vision_model TEXT NOT NULL,
    text_model TEXT,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    sort_index INTEGER NOT NULL,
    error_state TEXT,
    error_at INTEGER
);

CREATE TABLE ai_compat (
    provider_id INTEGER NOT NULL REFERENCES ai_providers (id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    json_mode TEXT NOT NULL DEFAULT 'schema' CHECK (json_mode IN ('schema', 'object', 'text')),
    send_temperature INTEGER NOT NULL DEFAULT 1 CHECK (send_temperature IN (0, 1)),
    max_tokens_param TEXT NOT NULL DEFAULT 'max_tokens' CHECK (max_tokens_param IN ('max_tokens', 'max_completion_tokens')),
    max_tokens_floor INTEGER,
    updated_at INTEGER,
    PRIMARY KEY (provider_id, model)
);

CREATE TABLE ai_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    at INTEGER,
    provider_id INTEGER,
    model TEXT,
    purpose TEXT CHECK (purpose IN ('title_screen', 'analysis', 'profile', 'market', 'test', 'probe')),
    task_id INTEGER,
    ok INTEGER CHECK (ok IN (0, 1)),
    http_status INTEGER,
    error_kind TEXT,
    json_mode TEXT CHECK (json_mode IN ('schema', 'object', 'text')),
    input_tokens INTEGER,
    output_tokens INTEGER,
    reasoning_tokens INTEGER,
    finish_reason TEXT,
    duration_ms INTEGER
);
CREATE INDEX ai_calls_at ON ai_calls (at);

CREATE TABLE deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    at INTEGER,
    channel_id INTEGER,
    task_id INTEGER,
    kind TEXT CHECK (kind IN ('recommended', 'all_new', 'price_drop', 'summary', 'system', 'test')),
    item_count INTEGER,
    ok INTEGER CHECK (ok IN (0, 1)),
    error TEXT
);
CREATE INDEX deliveries_at ON deliveries (at);
CREATE INDEX deliveries_channel_id_at ON deliveries (channel_id, at);

CREATE TABLE delivery_items (
    delivery_id INTEGER NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    finding_id INTEGER NOT NULL,
    PRIMARY KEY (delivery_id, finding_id)
);
CREATE INDEX delivery_items_finding_id ON delivery_items (finding_id);

CREATE TABLE run_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER REFERENCES tasks (id) ON DELETE SET NULL,
    task_name TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
    account_id INTEGER,
    account_name TEXT,
    proxy_name TEXT,
    started_at INTEGER,
    ended_at INTEGER,
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failed', 'skipped')),
    failure_kind TEXT CHECK (failure_kind IN ('risk_verify', 'risk_deny', 'risk_env', 'login', 'network', 'page', 'timeout', 'quota', 'config', 'cancelled', 'restart')),
    message TEXT,
    repeat_count INTEGER NOT NULL DEFAULT 1,
    page_count INTEGER,
    scanned_count INTEGER,
    new_count INTEGER,
    layer1_rejected INTEGER,
    layer2_count INTEGER,
    layer2_rejected INTEGER,
    detail_count INTEGER,
    seller_page_count INTEGER,
    analyzed_count INTEGER,
    recommended_count INTEGER,
    notified_count INTEGER,
    warnings_json TEXT
);
CREATE INDEX run_records_started_at ON run_records (started_at);
CREATE INDEX run_records_task_id_started_at ON run_records (task_id, started_at);

CREATE TABLE run_steps (
    run_id INTEGER NOT NULL REFERENCES run_records (id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    at INTEGER,
    status TEXT CHECK (status IN ('ok', 'warn', 'fail')),
    message TEXT,
    detail TEXT,
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE system_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT CHECK (kind IN ('account_verify', 'account_login', 'account_deny', 'ai_error', 'ai_budget', 'task_failure_pause', 'xianyu_layout', 'login_failures')),
    subject_id INTEGER,
    message TEXT,
    created_at INTEGER,
    resolved_at INTEGER,
    notified_at INTEGER
);
CREATE UNIQUE INDEX system_events_unresolved ON system_events (kind, subject_id) WHERE resolved_at IS NULL;

CREATE TABLE market_insights (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER REFERENCES tasks (id) ON DELETE CASCADE,
    generated_at INTEGER,
    stats_json TEXT,
    text TEXT,
    provider_id INTEGER,
    model TEXT
);
CREATE INDEX market_insights_task_id_generated_at ON market_insights (task_id, generated_at);

CREATE TABLE meta (
    key TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);
