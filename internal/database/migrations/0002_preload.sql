-- Preload records move from <state>/preload/<branch>/<version>/preload.json into
-- the database. The document is read and written as a whole by the preload state
-- machine, so the nested plans stay JSON; only the columns the rest of the program
-- queries (identity, state, recency) are real columns.
-- install_key is the absolute state root of one installation. Two installations
-- can hold a preload for the same branch and version at once, and their target
-- lists and blobs are not interchangeable, so the record identity includes it.
CREATE TABLE preload_records (
    install_key TEXT NOT NULL CHECK (length(install_key) BETWEEN 1 AND 1024),
    branch TEXT NOT NULL,
    version TEXT NOT NULL,
    task_id TEXT NOT NULL,
    apply_txn_id TEXT NOT NULL DEFAULT '',
    schema INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN (
        'unavailable', 'available', 'planning', 'downloading', 'paused',
        'verifying', 'staged', 'waitingRelease', 'reconcile', 'applicable',
        'applying', 'completed', 'stale', 'recoveryRequired', 'failed')),
    route TEXT NOT NULL CHECK (route IN ('Full', 'Patch', 'Mixed')),
    list_hash TEXT NOT NULL,
    saved_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    target_seen_at INTEGER,
    budget TEXT NOT NULL,
    observed_versions TEXT NOT NULL,
    targets TEXT NOT NULL,
    reused TEXT,
    files TEXT NOT NULL,
    patches TEXT,
    plan TEXT,
    PRIMARY KEY (install_key, branch, version)
) STRICT;

-- Listing the newest preload per branch drives the preload banner and the
-- discard sweep, which both run on startup.
CREATE INDEX preload_records_state ON preload_records (state, updated_at DESC);
