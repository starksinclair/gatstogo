DROP POLICY IF EXISTS plant_reviews_isolation ON plant_reviews;
DROP TRIGGER IF EXISTS trg_plant_reviews_updated_at ON plant_reviews;
DROP INDEX IF EXISTS idx_plant_reviews_ticket;
DROP INDEX IF EXISTS idx_plant_reviews_plant_submitted;
DROP TABLE IF EXISTS plant_reviews;
