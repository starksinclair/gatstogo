-- ============================================================================
-- Removes everything scripts/dev/seed-demo-data.sql added. LOCAL DEV ONLY.
--
--   docker exec -i gatstogo-postgres psql -U gatstogo -d gatstogo -v ON_ERROR_STOP=1 < scripts/dev/cleanup-demo-data.sql
--
-- Demo plants ("demo-..." slugs) are deleted outright; that cascades to
-- their users, tickets, shifts, cash, prices, pumps, hours, reviews, and
-- activity. Rows the seed added to the sunrise plant are listed in
-- zz_seed_registry and deleted individually, then the registry is dropped.
-- ============================================================================

BEGIN;

DELETE FROM plants WHERE slug LIKE 'demo-%';

DO $$
BEGIN
	IF to_regclass('public.zz_seed_registry') IS NULL THEN
		RAISE NOTICE 'No seed registry found; only demo plants were removed.';
		RETURN;
	END IF;

	-- children first, so no foreign key is left pointing at a deleted row
	DELETE FROM activity_log   WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'activity_log');
	DELETE FROM plant_reviews  WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'plant_reviews');
	DELETE FROM cash_movements WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'cash_movements');
	DELETE FROM tickets        WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'tickets');
	DELETE FROM shifts         WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'shifts');
	DELETE FROM prices         WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'prices');
	DELETE FROM plant_hours    WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'plant_hours');
	DELETE FROM pumps          WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'pumps');
	-- activity written later in the app by seeded staff (e.g. you opened a
	-- shift as one of them) would block deleting them; remove it too
	DELETE FROM activity_log   WHERE actor_id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'users');
	DELETE FROM users          WHERE id::text IN (SELECT id FROM zz_seed_registry WHERE tbl = 'users');

	DROP TABLE zz_seed_registry;
END $$;

COMMIT;

SELECT 'plants' AS what, count(*) AS how_many FROM plants
UNION ALL SELECT 'users', count(*) FROM users
UNION ALL SELECT 'tickets', count(*) FROM tickets;
