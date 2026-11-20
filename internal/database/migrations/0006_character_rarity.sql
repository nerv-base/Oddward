-- Character rarity ----------------------------------------------------------

-- The official name supplement only carries identifiers and names; the
-- community wiki states a character's rarity in the article infobox. Keep that
-- small table separate from the arc catalog so either source can be refreshed
-- or fail on its own. Rows are matched to a game identifier by the character's
-- official name, which is the page title on the wiki.
CREATE TABLE gacha_characters(
    name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
    rarity TEXT NOT NULL DEFAULT '' CHECK (length(rarity) <= 8),
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gacha_characters_meta(
    id INTEGER PRIMARY KEY CHECK (id = 1),
    fetched_at INTEGER NOT NULL
) STRICT;
