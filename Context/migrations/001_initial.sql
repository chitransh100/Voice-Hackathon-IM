CREATE TABLE users (
    glusr_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('buyer', 'seller')),
    business_name TEXT,
    city TEXT,
    profile_json TEXT NOT NULL DEFAULT '{}',
    profile_updated_at TEXT NOT NULL,
    PRIMARY KEY (glusr_id, role)
);

CREATE TABLE interactions (
    interaction_id TEXT PRIMARY KEY,
    glusr_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('buyer', 'seller')),
    channel TEXT NOT NULL CHECK (
        channel IN ('voice', 'whatsapp', 'web_chat', 'app', 'enquiry', 'other')
    ),
    source TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    source_revision INTEGER NOT NULL DEFAULT 1,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    session_state TEXT NOT NULL DEFAULT 'completed'
        CHECK (session_state IN ('ongoing', 'completed')),
    thread_id TEXT,
    summary TEXT,
    important_points_json TEXT NOT NULL DEFAULT '[]',
    source_payload_json TEXT NOT NULL DEFAULT '{}',
    processing_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'processing', 'processed', 'failed')),
    processed_revision INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_retry_at TEXT,
    last_error TEXT,
    FOREIGN KEY (glusr_id, role) REFERENCES users(glusr_id, role),
    UNIQUE (source, source_event_id, glusr_id, role)
);

CREATE INDEX idx_interactions_user_time
    ON interactions(glusr_id, role, occurred_at DESC, interaction_id);

CREATE INDEX idx_interactions_user_thread_time
    ON interactions(glusr_id, role, thread_id, occurred_at DESC);

CREATE INDEX idx_interactions_pending
    ON interactions(processing_status, next_retry_at, received_at);

CREATE TABLE current_context (
    glusr_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('buyer', 'seller')),
    memory_json TEXT NOT NULL DEFAULT '{}',
    context_md TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    updated_at TEXT NOT NULL,
    latest_source_event_at TEXT,
    token_count INTEGER,
    token_count_method TEXT NOT NULL,
    source_coverage_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (glusr_id, role),
    FOREIGN KEY (glusr_id, role) REFERENCES users(glusr_id, role)
);
