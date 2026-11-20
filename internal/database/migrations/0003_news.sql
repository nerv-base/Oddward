-- The official announcement feed is one small document that is always replaced
-- whole and never queried field by field, so it stays JSON in a single row; the
-- articles and banner images it points at are cached bodies described by
-- asset_cache rows (kind 'news-article'), not by this table.
CREATE TABLE news_feed (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    payload TEXT NOT NULL,
    fetched_at INTEGER NOT NULL
) STRICT;

-- One verified slogan image per launcher build. The image bytes stay on disk
-- under a name derived from md5, so a row always names exactly the pixels it
-- describes and a crash between the file write and the commit cannot pair new
-- pixels with old metadata.
CREATE TABLE branding_record (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    cover_code TEXT NOT NULL,
    url TEXT NOT NULL,
    etag TEXT NOT NULL,
    md5 TEXT NOT NULL,
    bytes INTEGER NOT NULL CHECK (bytes >= 0),
    width INTEGER NOT NULL CHECK (width >= 0),
    height INTEGER NOT NULL CHECK (height >= 0),
    last_modified INTEGER,
    version_number TEXT NOT NULL,
    version_name TEXT NOT NULL,
    article_url TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    image_path TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
