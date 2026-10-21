ALTER TABLE users
    ADD COLUMN can_manage_pumps BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE plant_hours (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id    UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    day_of_week INTEGER NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    segment     INTEGER NOT NULL DEFAULT 1 CHECK (segment BETWEEN 1 AND 3),
    opens_at    TIME,
    closes_at   TIME,
    closed      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plant_id, day_of_week, segment)
);

CREATE TABLE pumps (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id    UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'working'
        CHECK (status IN ('working', 'not_working', 'maintenance')),
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plant_id, name)
);

ALTER TABLE shifts
    ADD COLUMN pump_id UUID REFERENCES pumps(id);

ALTER TABLE tickets
    ADD COLUMN pump_id UUID REFERENCES pumps(id),
    ADD COLUMN dispensing_started_at TIMESTAMPTZ,
    ADD COLUMN dispensing_started_by UUID REFERENCES users(id),
    ADD COLUMN closed_by UUID REFERENCES users(id),
    ADD COLUMN close_note TEXT;

ALTER TABLE tickets DROP CONSTRAINT tickets_status_check;
ALTER TABLE tickets
    ADD CONSTRAINT tickets_status_check
    CHECK (status IN ('pending', 'paid', 'dispensing', 'filled', 'issue', 'cancelled', 'expired', 'voided'));

DROP INDEX IF EXISTS idx_tickets_open_code;
CREATE UNIQUE INDEX idx_tickets_open_code
    ON tickets (plant_id, code)
    WHERE status IN ('pending', 'paid', 'dispensing');

CREATE INDEX idx_plant_hours_plant_day ON plant_hours (plant_id, day_of_week, segment);
CREATE INDEX idx_pumps_plant_status ON pumps (plant_id, status, sort_order);
CREATE INDEX idx_shifts_open_pump ON shifts (plant_id, pump_id) WHERE closed_at IS NULL;
CREATE INDEX idx_tickets_pump_status ON tickets (plant_id, pump_id, status);

CREATE TRIGGER trg_plant_hours_updated_at
    BEFORE UPDATE ON plant_hours
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_pumps_updated_at
    BEFORE UPDATE ON pumps
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE plant_hours ENABLE ROW LEVEL SECURITY;
ALTER TABLE pumps ENABLE ROW LEVEL SECURITY;
ALTER TABLE plant_hours FORCE ROW LEVEL SECURITY;
ALTER TABLE pumps FORCE ROW LEVEL SECURITY;

CREATE POLICY plant_hours_isolation ON plant_hours
    USING (plant_id = current_plant_id());

CREATE POLICY pumps_isolation ON pumps
    USING (plant_id = current_plant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON plant_hours, pumps TO gatstogo_app;
GRANT ALL PRIVILEGES ON plant_hours, pumps TO gatstogo_admin;

INSERT INTO pumps (plant_id, name, sort_order)
SELECT id, 'Pump 1', 1
FROM plants p
WHERE NOT EXISTS (SELECT 1 FROM pumps pu WHERE pu.plant_id = p.id);
