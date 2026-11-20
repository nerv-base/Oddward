-- Pull provenance -----------------------------------------------------------

-- A draw now keeps the rarity and the interface it was observed through.
-- pity_known separates "the source reported no counter" (the arc pool) from a
-- real zero, because the existing pity column is NOT NULL with a non-negative
-- check. Every row written before this migration came from the official
-- interface, which lists only S items.
ALTER TABLE gacha_records ADD COLUMN pity_known INTEGER NOT NULL DEFAULT 1 CHECK (pity_known IN (0, 1));
ALTER TABLE gacha_records ADD COLUMN rank TEXT NOT NULL DEFAULT '' CHECK (length(rank) <= 8);
ALTER TABLE gacha_records ADD COLUMN origin TEXT NOT NULL DEFAULT '' CHECK (length(origin) <= 16);
ALTER TABLE gacha_records ADD COLUMN uid TEXT NOT NULL DEFAULT '' CHECK (length(uid) <= 64);

UPDATE gacha_records SET rank = 'S', origin = 'tajiduo' WHERE rank = '';
