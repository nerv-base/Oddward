-- SPDX-License-Identifier: GPL-3.0-or-later

-- Hethereau Gallery (海特洛相册): local media discovery, indexing, multi-source
-- locations, content-addressed archive tracking and gallery preferences.
-- Media bytes remain on the filesystem; SQLite holds only metadata, digests,
-- locations and archive status. Timestamps are UTC Unix nanoseconds (NULL when
-- unknown; file modified_at never substitutes for an unknown captured_at).

CREATE TABLE gallery_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    auto_archive       INTEGER NOT NULL CHECK (auto_archive IN (0, 1)),
    archive_dir        TEXT NOT NULL CHECK (length(archive_dir) <= 1024),
    auto_discover_game INTEGER NOT NULL CHECK (auto_discover_game IN (0, 1)),
    thumb_cache_mb     INTEGER NOT NULL CHECK (thumb_cache_mb BETWEEN 32 AND 4096),
    updated_at         INTEGER NOT NULL
) STRICT;

CREATE TABLE gallery_sources (
    id           INTEGER PRIMARY KEY,
    root_path    TEXT NOT NULL UNIQUE CHECK (length(root_path) BETWEEN 1 AND 1024),
    source_type  TEXT NOT NULL CHECK (source_type IN ('game_cache', 'exported', 'custom')),
    channel      TEXT,
    uid          TEXT CHECK (uid IS NULL OR length(uid) BETWEEN 1 AND 64),
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    last_scan_at INTEGER
) STRICT;

CREATE INDEX gallery_sources_type ON gallery_sources(source_type, enabled);

CREATE TABLE gallery_assets (
    id             INTEGER PRIMARY KEY,
    sha256         TEXT NOT NULL UNIQUE CHECK (length(sha256) = 64),
    media_type     TEXT NOT NULL CHECK (media_type IN ('image', 'video')),
    mime_type      TEXT NOT NULL CHECK (length(mime_type) BETWEEN 1 AND 64),
    byte_size      INTEGER NOT NULL CHECK (byte_size >= 0),
    width          INTEGER NOT NULL CHECK (width >= 0),
    height         INTEGER NOT NULL CHECK (height >= 0),
    duration_ms    INTEGER,
    captured_at    INTEGER,
    discovered_at  INTEGER NOT NULL,
    invalid        INTEGER NOT NULL DEFAULT 0 CHECK (invalid IN (0, 1)),
    invalid_reason TEXT NOT NULL DEFAULT '' CHECK (length(invalid_reason) <= 128)
) STRICT;

CREATE INDEX gallery_assets_captured ON gallery_assets(captured_at DESC, discovered_at DESC);

CREATE TABLE gallery_asset_locations (
    id            INTEGER PRIMARY KEY,
    asset_id      INTEGER NOT NULL REFERENCES gallery_assets(id) ON DELETE CASCADE,
    source_id     INTEGER NOT NULL REFERENCES gallery_sources(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL CHECK (length(relative_path) BETWEEN 1 AND 1024),
    file_size     INTEGER NOT NULL CHECK (file_size >= 0),
    modified_at   INTEGER NOT NULL,
    file_identity TEXT CHECK (file_identity IS NULL OR length(file_identity) <= 128),
    available     INTEGER NOT NULL CHECK (available IN (0, 1)),
    last_seen_at  INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX gallery_asset_locations_source_rel ON gallery_asset_locations(source_id, relative_path);
CREATE INDEX gallery_asset_locations_asset ON gallery_asset_locations(asset_id, available);
CREATE INDEX gallery_asset_locations_modified ON gallery_asset_locations(modified_at DESC);

CREATE TABLE gallery_archive (
    asset_id      INTEGER PRIMARY KEY REFERENCES gallery_assets(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL UNIQUE CHECK (length(relative_path) BETWEEN 1 AND 256),
    archived_at   INTEGER NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('ready', 'missing', 'corrupt'))
) STRICT;
