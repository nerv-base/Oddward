-- SPDX-License-Identifier: GPL-3.0-or-later

-- Hethereau Gallery P1: favorites, custom albums, tags, verified video codec
-- metadata, companion keyframe poster tracking and validated custom static
-- launcher background persistence.

ALTER TABLE gallery_settings ADD COLUMN custom_background TEXT NOT NULL DEFAULT '' CHECK (length(custom_background) <= 1024);

ALTER TABLE gallery_assets ADD COLUMN video_codec TEXT NOT NULL DEFAULT '' CHECK (length(video_codec) <= 32);
ALTER TABLE gallery_assets ADD COLUMN audio_codec TEXT NOT NULL DEFAULT '' CHECK (length(audio_codec) <= 32);
ALTER TABLE gallery_assets ADD COLUMN poster_path TEXT NOT NULL DEFAULT '' CHECK (length(poster_path) <= 1024);

CREATE TABLE gallery_favorites (
    asset_id     INTEGER PRIMARY KEY REFERENCES gallery_assets(id) ON DELETE CASCADE,
    favorited_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gallery_albums (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE CHECK (length(trim(name)) BETWEEN 1 AND 64),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gallery_album_items (
    album_id INTEGER NOT NULL REFERENCES gallery_albums(id) ON DELETE CASCADE,
    asset_id INTEGER NOT NULL REFERENCES gallery_assets(id) ON DELETE CASCADE,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (album_id, asset_id)
) STRICT;

CREATE INDEX gallery_album_items_asset ON gallery_album_items(asset_id);

CREATE TABLE gallery_tags (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE CHECK (length(trim(name)) BETWEEN 1 AND 40),
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gallery_asset_tags (
    asset_id INTEGER NOT NULL REFERENCES gallery_assets(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES gallery_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (asset_id, tag_id)
) STRICT;

CREATE INDEX gallery_asset_tags_tag ON gallery_asset_tags(tag_id);
