-- ======================
-- Drop policies first
-- ======================
DROP POLICY IF EXISTS plants_isolation ON plants;
DROP POLICY IF EXISTS users_isolation ON users;
DROP POLICY IF EXISTS prices_isolation ON prices;
DROP POLICY IF EXISTS shifts_isolation ON shifts;
DROP POLICY IF EXISTS tickets_isolation ON tickets;
DROP POLICY IF EXISTS cash_movements_isolation ON cash_movements;
DROP POLICY IF EXISTS activity_log_isolation ON activity_log;

-- ======================
-- Drop helper function
-- ======================
DROP FUNCTION IF EXISTS current_plant_id();

-- ======================
-- Drop triggers
-- ======================
DROP TRIGGER IF EXISTS trg_plants_updated_at ON plants;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP TRIGGER IF EXISTS trg_shifts_updated_at ON shifts;
DROP TRIGGER IF EXISTS trg_tickets_updated_at ON tickets;

DROP FUNCTION IF EXISTS set_updated_at();

-- ======================
-- Drop tables (order matters because of foreign keys)
-- ======================
DROP TABLE IF EXISTS activity_log;
DROP TABLE IF EXISTS cash_movements;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS shifts;
DROP TABLE IF EXISTS prices;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS plants;
DROP TABLE IF EXISTS reserved_slugs;

-- ======================
-- Drop roles (optional - be careful in production)
-- ======================
-- Uncomment these only if you want the down migration to also remove the roles
-- DROP ROLE IF EXISTS gatstogo_app;
-- DROP ROLE IF EXISTS gatstogo_admin;