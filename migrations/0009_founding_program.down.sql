DROP TABLE IF EXISTS buyer_signups;
DROP INDEX IF EXISTS idx_plants_founding_rank;
ALTER TABLE plants DROP COLUMN IF EXISTS founding_rank;
