-- SPDX-License-Identifier: GPL-3.0-or-later

-- One row per legacy file, not one per domain.
--
-- Gacha keeps a document per role, downloads one per task, and installs and
-- preloads one per installation. With a domain-wide primary key only the file
-- written last was tracked, so a rollback that edited an older file went
-- unnoticed and its edits were not reconciled back in. The primary key now
-- includes the path, and MirrorStale treats any changed file as stale.

CREATE TABLE compat_mirrors_v4 (
    domain      TEXT NOT NULL,
    path        TEXT NOT NULL CHECK (length(path) BETWEEN 1 AND 1024),
    digest      TEXT NOT NULL,
    mirrored_at INTEGER NOT NULL,
    PRIMARY KEY (domain, path)
) STRICT;

INSERT INTO compat_mirrors_v4(domain, path, digest, mirrored_at)
    SELECT domain, path, digest, mirrored_at FROM compat_mirrors;

DROP TABLE compat_mirrors;

ALTER TABLE compat_mirrors_v4 RENAME TO compat_mirrors;

CREATE INDEX compat_mirrors_domain ON compat_mirrors(domain, mirrored_at DESC);
