-- Every mutable table carries updated_at + is_synced. The Firebase sweep polls
-- for is_synced = 0, ships those rows, then flips the flag. Any write path that
-- forgets to reset is_synced silently drops the row out of the sweep, so the
-- flag is set in the same UPDATE as the data it describes -- never afterwards.

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    timezone        TEXT    NOT NULL DEFAULT 'UTC',
    goal_kcal       INTEGER NOT NULL,
    goal_protein_g  INTEGER NOT NULL,
    goal_carbs_g    INTEGER NOT NULL,
    goal_fat_g      INTEGER NOT NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    is_synced       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS characters (
    user_id     TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    level       INTEGER NOT NULL DEFAULT 1,
    xp          INTEGER NOT NULL DEFAULT 0,
    con_micro   INTEGER NOT NULL DEFAULT 5000,
    vit_micro   INTEGER NOT NULL DEFAULT 5000,
    updated_at  TEXT    NOT NULL,
    is_synced   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS streaks (
    user_id          TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    current_streak   INTEGER NOT NULL DEFAULT 0,
    longest_streak   INTEGER NOT NULL DEFAULT 0,
    last_local_date  TEXT,
    last_activity_at TEXT,
    updated_at       TEXT    NOT NULL,
    is_synced        INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS macro_entries (
    id                      TEXT PRIMARY KEY,
    user_id                 TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_entry_id         TEXT    NOT NULL,
    local_date              TEXT    NOT NULL,
    kcal                    INTEGER NOT NULL,
    protein_g               INTEGER NOT NULL,
    carbs_g                 INTEGER NOT NULL,
    fat_g                   INTEGER NOT NULL,
    source                  TEXT    NOT NULL CHECK (source IN ('photo','reattempt','manual')),
    is_manual               INTEGER NOT NULL DEFAULT 0,
    ai_confidence           REAL,
    ai_model                TEXT,
    photo_uri               TEXT,
    supersedes_rejection_id TEXT REFERENCES intake_rejections(id),
    logged_at               TEXT    NOT NULL,
    created_at              TEXT    NOT NULL,
    updated_at              TEXT    NOT NULL,
    is_synced               INTEGER NOT NULL DEFAULT 0,
    -- Idempotency: a mobile client retrying over a flaky link must not double-log.
    UNIQUE (user_id, client_entry_id)
);

CREATE INDEX IF NOT EXISTS idx_entries_user_date ON macro_entries (user_id, local_date);
CREATE INDEX IF NOT EXISTS idx_entries_dirty     ON macro_entries (is_synced) WHERE is_synced = 0;

CREATE TABLE IF NOT EXISTS intake_rejections (
    id                   TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_entry_id      TEXT NOT NULL,
    validation_reasoning TEXT NOT NULL,
    ai_confidence        REAL,
    ai_model             TEXT,
    photo_uri            TEXT,
    resolved_by_entry_id TEXT REFERENCES macro_entries(id),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    is_synced            INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_rejections_user ON intake_rejections (user_id, created_at);

CREATE TABLE IF NOT EXISTS daily_macro_totals (
    user_id     TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    local_date  TEXT    NOT NULL,
    kcal        INTEGER NOT NULL DEFAULT 0,
    protein_g   INTEGER NOT NULL DEFAULT 0,
    carbs_g     INTEGER NOT NULL DEFAULT 0,
    fat_g       INTEGER NOT NULL DEFAULT 0,
    entry_count INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT    NOT NULL,
    is_synced   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, local_date)
);
