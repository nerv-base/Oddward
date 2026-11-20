-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Oddward structured storage, schema version 1.
--
-- Conventions used by every table below:
--
--   * Timestamps are unix nanoseconds in UTC stored as INTEGER, except where the
--     domain itself works in milliseconds (gacha identity) or in calendar days
--     (check-in dates). The zero time is NULL, never 0.
--   * Content-addressed game objects are identified by "md5.size", the same key
--     the download cache uses for its blob files.
--   * Structured fields that are queried, constrained or updated in place get
--     real columns. Only nested variable-length structures that the domain
--     always reads and writes as a whole (checksum blocks, request marks, patch
--     steps) are stored as JSON text.
--   * Every table is STRICT so a mistyped value cannot be silently coerced.

-- Bookkeeping ---------------------------------------------------------------

CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;

CREATE TABLE app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- One row per domain that imports legacy files. Kept in the database so an
-- interrupted migration is detected and resumed instead of being repeated or
-- skipped.
CREATE TABLE migration_state (
    domain      TEXT PRIMARY KEY,
    status      TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed')),
    source      TEXT NOT NULL,
    detail      TEXT NOT NULL,
    version     INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
) STRICT;

-- Legacy files this build still rewrites as a rollback mirror. The digest is of
-- what Oddward last wrote; if the file on disk differs at startup, an older build
-- ran and changed it, so the importer reconciles it before it can be ignored.
CREATE TABLE compat_mirrors (
    domain      TEXT PRIMARY KEY,
    path        TEXT NOT NULL,
    digest      TEXT NOT NULL,
    mirrored_at INTEGER NOT NULL
) STRICT;

-- Launcher settings ---------------------------------------------------------

CREATE TABLE launch_settings (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    source_version   INTEGER NOT NULL,
    game_root        TEXT NOT NULL,
    risk_version     TEXT NOT NULL,
    skipped_version  TEXT NOT NULL,
    proxy            TEXT NOT NULL,
    proxy_url        TEXT NOT NULL,
    mirror           INTEGER NOT NULL CHECK (mirror IN (0, 1)),
    mirror_prefix    TEXT NOT NULL,
    rate_limit_kib   INTEGER NOT NULL CHECK (rate_limit_kib >= 0),
    ui_close_to_tray INTEGER NOT NULL CHECK (ui_close_to_tray IN (0, 1)),
    ui_accent_follow INTEGER NOT NULL CHECK (ui_accent_follow IN (0, 1)),
    ui_check_updates INTEGER NOT NULL CHECK (ui_check_updates IN (0, 1)),
    updated_at       INTEGER NOT NULL
) STRICT;

-- Gacha archives ------------------------------------------------------------

CREATE TABLE gacha_archives (
    role_id     TEXT PRIMARY KEY,
    source      TEXT NOT NULL,
    imported_at INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

-- Identity of a record is (archive, pool, item, millisecond, pity); luck is an
-- attribute that can be upgraded from unknown, so it is not part of the key.
CREATE TABLE gacha_records (
    id         INTEGER PRIMARY KEY,
    role_id    TEXT NOT NULL REFERENCES gacha_archives(role_id) ON DELETE CASCADE,
    pool       TEXT NOT NULL CHECK (pool IN ('limited', 'permanent', 'weapon')),
    item_id    TEXT NOT NULL CHECK (length(item_id) BETWEEN 1 AND 64),
    at_ms      INTEGER NOT NULL CHECK (at_ms > 0),
    pity       INTEGER NOT NULL CHECK (pity >= 0),
    luck       INTEGER NOT NULL CHECK (luck BETWEEN -1 AND 4),
    created_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX gacha_records_identity ON gacha_records(role_id, pool, item_id, at_ms, pity);
CREATE INDEX gacha_records_recent ON gacha_records(role_id, at_ms DESC);
CREATE INDEX gacha_records_item ON gacha_records(item_id);

-- Snapshot metadata is deduplicated by the domain's own canonical key, so the
-- database enforces exactly what normalizeMetas deduplicated in memory.
CREATE TABLE gacha_snapshots (
    id          INTEGER PRIMARY KEY,
    role_id     TEXT NOT NULL REFERENCES gacha_archives(role_id) ON DELETE CASCADE,
    meta_key    TEXT NOT NULL,
    fetched_at  INTEGER NOT NULL,
    luck_type   INTEGER NOT NULL,
    luck_title  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (role_id, meta_key)
) STRICT;

CREATE TABLE gacha_snapshot_pools (
    snapshot_id   INTEGER NOT NULL REFERENCES gacha_snapshots(id) ON DELETE CASCADE,
    pool          TEXT NOT NULL CHECK (pool IN ('limited', 'permanent', 'weapon')),
    m             INTEGER NOT NULL,
    draw_count    INTEGER NOT NULL,
    rare_count    INTEGER NOT NULL,
    average       TEXT NOT NULL,
    player_over   TEXT NOT NULL,
    rule_mismatch INTEGER NOT NULL CHECK (rule_mismatch IN (0, 1)),
    PRIMARY KEY (snapshot_id, pool)
) STRICT;

-- Official item id to display name, fed by the gacha sync.
CREATE TABLE gacha_item_names (
    item_id    TEXT PRIMARY KEY CHECK (length(item_id) BETWEEN 1 AND 64),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    updated_at INTEGER NOT NULL
) STRICT;

-- BWIKI arc catalog: id to name and rarity.
CREATE TABLE gacha_arcs (
    item_id    TEXT PRIMARY KEY CHECK (length(item_id) BETWEEN 1 AND 72),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    rarity     TEXT NOT NULL CHECK (length(rarity) <= 8),
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gacha_arcs_meta (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    fetched_at INTEGER NOT NULL
) STRICT;

-- Accounts ------------------------------------------------------------------

-- The sealed record is stored exactly as it is written to disk: its fields are
-- bound into the AES-GCM associated data, so the canonical bytes are the only
-- representation that can be authenticated. The extra columns exist to list,
-- order and deduplicate without decrypting anything.
CREATE TABLE accounts (
    id           TEXT PRIMARY KEY CHECK (length(id) = 32),
    label        TEXT NOT NULL,
    identity_key TEXT NOT NULL,
    saved_at     INTEGER NOT NULL,
    protection   TEXT NOT NULL,
    record       BLOB NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX accounts_identity ON accounts(identity_key);
CREATE INDEX accounts_saved_at ON accounts(saved_at DESC, id);

CREATE TABLE account_tajiduo (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    center_uid TEXT NOT NULL,
    file       BLOB NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Device-level account state. last_account is NULL until the user has chosen an
-- account, and the empty string when nothing is selected; that distinction is
-- why it is not a NOT NULL column.
CREATE TABLE account_state (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    subject_key     BLOB,
    last_account    TEXT CHECK (last_account IS NULL OR length(last_account) = 32),
    notice_revision INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;

-- Check-in ------------------------------------------------------------------

CREATE TABLE checkin_records (
    account_id TEXT NOT NULL CHECK (length(account_id) BETWEEN 1 AND 64),
    -- Empty is the account-scoped community check-in: it has no role, and the
    -- legacy ledger wrote "" for it too.
    role_id    TEXT NOT NULL CHECK (length(role_id) BETWEEN 0 AND 64),
    kind       INTEGER NOT NULL CHECK (kind IN (0, 1)),
    date       TEXT NOT NULL CHECK (length(date) = 10),
    result     INTEGER NOT NULL CHECK (result BETWEEN 0 AND 4),
    detail     TEXT NOT NULL CHECK (length(detail) <= 64),
    at         INTEGER NOT NULL,
    PRIMARY KEY (account_id, role_id, kind, date)
) STRICT;

CREATE INDEX checkin_records_date ON checkin_records(date);

CREATE TABLE checkin_auto (
    account_id TEXT NOT NULL CHECK (length(account_id) BETWEEN 1 AND 64),
    kind       INTEGER NOT NULL CHECK (kind IN (0, 1)),
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    PRIMARY KEY (account_id, kind)
) STRICT;

-- Game downloads ------------------------------------------------------------

CREATE TABLE download_tasks (
    id             TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    schema         INTEGER NOT NULL,
    install_key    TEXT NOT NULL,
    purpose        TEXT NOT NULL,
    cache_root     TEXT NOT NULL,
    branch         TEXT NOT NULL,
    target_version TEXT NOT NULL,
    list_hash      TEXT NOT NULL,
    kind           INTEGER NOT NULL CHECK (kind BETWEEN 1 AND 4),
    state          INTEGER NOT NULL CHECK (state BETWEEN 1 AND 8),
    err            TEXT NOT NULL,
    network_bytes  INTEGER NOT NULL,
    written_bytes  INTEGER NOT NULL,
    reused_bytes   INTEGER NOT NULL,
    cached_bytes   INTEGER NOT NULL,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

CREATE INDEX download_tasks_state ON download_tasks(state, updated_at);

-- One row per transfer so progress is a single-row update instead of a rewrite of
-- the whole task. object_key is indexed because cache GC resolves the blobs that
-- a task still references through it.
CREATE TABLE download_transfers (
    task_id              TEXT NOT NULL REFERENCES download_tasks(id) ON DELETE CASCADE,
    ordinal              INTEGER NOT NULL,
    object_md5           TEXT NOT NULL CHECK (length(object_md5) = 32),
    object_size          INTEGER NOT NULL CHECK (object_size > 0),
    object_key           TEXT NOT NULL,
    source_kind          INTEGER NOT NULL CHECK (source_kind BETWEEN 1 AND 3),
    source_md5           TEXT NOT NULL,
    source_size          INTEGER NOT NULL,
    source_offset        INTEGER NOT NULL CHECK (source_offset >= 0),
    source_length        INTEGER NOT NULL CHECK (source_length >= 0),
    chunk_size           INTEGER NOT NULL CHECK (chunk_size >= 0),
    bytes_done           INTEGER NOT NULL CHECK (bytes_done >= 0),
    state                INTEGER NOT NULL CHECK (state BETWEEN 1 AND 4),
    patch_blocks_trusted INTEGER NOT NULL CHECK (patch_blocks_trusted IN (0, 1)),
    blocks               TEXT NOT NULL,
    marks                TEXT NOT NULL,
    updated_at           INTEGER NOT NULL,
    PRIMARY KEY (task_id, ordinal)
) STRICT;

CREATE INDEX download_transfers_object ON download_transfers(object_key);

CREATE TABLE download_rebuilds (
    task_id     TEXT NOT NULL REFERENCES download_tasks(id) ON DELETE CASCADE,
    ordinal     INTEGER NOT NULL,
    target_md5  TEXT NOT NULL,
    target_size INTEGER NOT NULL,
    output      TEXT NOT NULL,
    state       INTEGER NOT NULL CHECK (state BETWEEN 1 AND 6),
    steps       TEXT NOT NULL,
    PRIMARY KEY (task_id, ordinal)
) STRICT;

CREATE TABLE download_targets (
    task_id    TEXT NOT NULL REFERENCES download_tasks(id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    object_md5 TEXT NOT NULL,
    object_size INTEGER NOT NULL,
    action     INTEGER NOT NULL CHECK (action BETWEEN 1 AND 4),
    state      INTEGER NOT NULL CHECK (state BETWEEN 1 AND 5),
    PRIMARY KEY (task_id, path)
) STRICT;

-- Installed games -----------------------------------------------------------

CREATE TABLE install_records (
    install_key       TEXT PRIMARY KEY,
    schema            INTEGER NOT NULL,
    root              TEXT NOT NULL,
    mode              TEXT NOT NULL,
    state             TEXT NOT NULL,
    branch            TEXT NOT NULL,
    version           TEXT NOT NULL,
    list_hash         TEXT NOT NULL,
    manifest_digest   TEXT NOT NULL,
    txn_id            TEXT NOT NULL,
    base_version      TEXT NOT NULL,
    verify_coverage   TEXT NOT NULL,
    verified_targets  INTEGER NOT NULL,
    published_at      INTEGER,
    created_at        INTEGER,
    root_file_id      TEXT NOT NULL,
    official_digest   TEXT NOT NULL,
    baseline_mismatch INTEGER NOT NULL CHECK (baseline_mismatch IN (0, 1)),
    updated_at        INTEGER NOT NULL
) STRICT;

CREATE TABLE install_targets (
    install_key TEXT NOT NULL REFERENCES install_records(install_key) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    md5         TEXT NOT NULL,
    size        INTEGER NOT NULL,
    PRIMARY KEY (install_key, path)
) STRICT;

-- Media caches --------------------------------------------------------------
--
-- Images, video and audio stay on disk; only the index that describes them lives
-- here. expires_at in the past is a negative cache entry (a 404 the source asked
-- us to remember). A row whose file is gone is recoverable: the fetch path treats
-- a missing file as a miss and rewrites the row.

CREATE TABLE asset_cache (
    kind          TEXT NOT NULL,
    key           TEXT NOT NULL,
    path          TEXT NOT NULL,
    size          INTEGER NOT NULL,
    digest        TEXT NOT NULL,
    source_url    TEXT NOT NULL,
    etag          TEXT NOT NULL,
    last_modified TEXT NOT NULL,
    fetched_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_used     INTEGER NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('ok', 'missing', 'stale')),
    detail        TEXT NOT NULL,
    PRIMARY KEY (kind, key)
) STRICT;

CREATE INDEX asset_cache_last_used ON asset_cache(last_used);
CREATE INDEX asset_cache_expiry ON asset_cache(expires_at);
