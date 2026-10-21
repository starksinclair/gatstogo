DELETE FROM pumps
WHERE name = 'Pump 1'
  AND sort_order = 1
  AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.pump_id = pumps.id)
  AND NOT EXISTS (SELECT 1 FROM shifts s WHERE s.pump_id = pumps.id);

DROP POLICY IF EXISTS plant_hours_isolation ON plant_hours;
DROP POLICY IF EXISTS pumps_isolation ON pumps;

ALTER TABLE plant_hours DISABLE ROW LEVEL SECURITY;
ALTER TABLE pumps DISABLE ROW LEVEL SECURITY;

DROP TRIGGER IF EXISTS trg_plant_hours_updated_at ON plant_hours;
DROP TRIGGER IF EXISTS trg_pumps_updated_at ON pumps;

DROP INDEX IF EXISTS idx_plant_hours_plant_day;
DROP INDEX IF EXISTS idx_pumps_plant_status;
DROP INDEX IF EXISTS idx_shifts_open_pump;
DROP INDEX IF EXISTS idx_tickets_pump_status;

DROP INDEX IF EXISTS idx_tickets_open_code;
CREATE UNIQUE INDEX idx_tickets_open_code
    ON tickets (plant_id, code)
    WHERE status IN ('pending', 'paid');

ALTER TABLE tickets DROP CONSTRAINT tickets_status_check;
ALTER TABLE tickets
    ADD CONSTRAINT tickets_status_check
    CHECK (status IN ('pending', 'paid', 'filled', 'expired', 'voided'));

ALTER TABLE tickets
    DROP COLUMN IF EXISTS close_note,
    DROP COLUMN IF EXISTS closed_by,
    DROP COLUMN IF EXISTS dispensing_started_by,
    DROP COLUMN IF EXISTS dispensing_started_at,
    DROP COLUMN IF EXISTS pump_id;

ALTER TABLE shifts
    DROP COLUMN IF EXISTS pump_id;

DROP TABLE IF EXISTS pumps;
DROP TABLE IF EXISTS plant_hours;

ALTER TABLE users
    DROP COLUMN IF EXISTS can_manage_pumps;
