CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ======================
-- 1. Plants (Tenants)
-- ======================
CREATE TABLE plants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            TEXT UNIQUE NOT NULL,
    name            TEXT NOT NULL,
    city            TEXT,
    address         TEXT,
    phone           TEXT,
    logo_path       TEXT,

    custom_domain   TEXT UNIQUE,
    domain_status   TEXT NOT NULL DEFAULT 'none'
    CHECK (domain_status IN ('none', 'pending', 'active', 'broken')),

    status          TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('provisioning', 'active', 'suspended', 'closed')),

    primary_color     TEXT NOT NULL DEFAULT '#2563eb',
    secondary_color   TEXT NOT NULL DEFAULT '#1e40af',
    button_color      TEXT NOT NULL DEFAULT '#2563eb',
    button_text_color TEXT NOT NULL DEFAULT '#ffffff',
    font_family        TEXT NOT NULL DEFAULT 'Inter, sans-serif',

    timezone        TEXT NOT NULL DEFAULT 'Africa/Lagos',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_plants_slug ON plants(slug);
CREATE INDEX idx_plants_custom_domain ON plants(custom_domain) WHERE custom_domain IS NOT NULL;
CREATE INDEX idx_plants_status ON plants(status);

-- Reserved slugs
CREATE TABLE reserved_slugs (
                                slug TEXT PRIMARY KEY
);

INSERT INTO reserved_slugs (slug) VALUES
    ('www'), ('api'), ('admin'), ('app'), ('mail'), ('email'), ('smtp'),
    ('static'), ('cdn'), ('assets'), ('status'), ('help'), ('support'),
    ('billing'), ('docs'), ('blog'), ('dashboard'), ('terminal'), ('owner');

-- ======================
-- 2. Users
-- ======================
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id        UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    phone           TEXT NOT NULL,
    role            TEXT NOT NULL
       CHECK (role IN ('owner', 'manager', 'cashier', 'operator', 'admin')),
    pin_hash        TEXT,
    password_hash   TEXT,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plant_id, phone)
);

CREATE INDEX idx_users_plant_id ON users(plant_id);
CREATE INDEX idx_users_role ON users(role);
CREATE INDEX idx_users_phone ON users(phone);

-- ======================
-- 3. Prices (dated, never overwritten)
-- ======================
CREATE TABLE prices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id        UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    price_per_kg    BIGINT NOT NULL CHECK (price_per_kg > 0),  -- in kobo
    effective_from  TIMESTAMPTZ NOT NULL DEFAULT now(),
    set_by          UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prices_lookup ON prices (plant_id, effective_from DESC);

-- ======================
-- 4. Shifts
-- ======================
CREATE TABLE shifts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id            UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id),
    device_id           TEXT,

    opened_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at           TIMESTAMPTZ,

    opening_cash_kobo   BIGINT NOT NULL DEFAULT 0,
    closing_cash_kobo   BIGINT,

    tokens_issued       INTEGER NOT NULL DEFAULT 0,
    tokens_returned     INTEGER,

    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_shifts_plant_opened ON shifts (plant_id, opened_at DESC);
CREATE INDEX idx_shifts_user ON shifts (plant_id, user_id);

-- One open shift per user per plant
CREATE UNIQUE INDEX idx_shifts_one_open_per_user
    ON shifts (plant_id, user_id)
    WHERE closed_at IS NULL;

-- ======================
-- 5. Tickets (main transaction table)
-- ======================
CREATE TABLE tickets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id            UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    reference           TEXT NOT NULL,
    code                TEXT NOT NULL,

    customer_name       TEXT,
    customer_phone      TEXT,

    size_grams          INTEGER NOT NULL CHECK (size_grams > 0),
    rate_per_kg         BIGINT NOT NULL,                    -- snapshot of price at sale time (kobo)
    price_id            UUID REFERENCES prices(id),
    amount              BIGINT NOT NULL,

    channel             TEXT NOT NULL
     CHECK (channel IN ('transfer', 'terminal', 'ussd', 'cash')),

    status              TEXT NOT NULL DEFAULT 'pending'
     CHECK (status IN ('pending', 'paid', 'filled', 'expired', 'voided')),

    -- Actual dispensed amount
    net_grams           INTEGER,
    empty_weight_grams  INTEGER,
    filled_weight_grams INTEGER,

    filled_at           TIMESTAMPTZ,
    filled_shift_id     UUID REFERENCES shifts(id),

    confirmed_at        TIMESTAMPTZ,
    paid_at             TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ,

    sold_by             UUID REFERENCES users(id),
    shift_id            UUID REFERENCES shifts(id),

    void_reason         TEXT,
    voided_by           UUID REFERENCES users(id),
    voided_at           TIMESTAMPTZ,

    idempotency_key     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plant_id, reference)
);

-- Code only needs to be unique while it is still redeemable
CREATE UNIQUE INDEX idx_tickets_open_code
    ON tickets (plant_id, code)
    WHERE status IN ('pending', 'paid');

CREATE INDEX idx_tickets_plant_created ON tickets (plant_id, created_at DESC);
CREATE INDEX idx_tickets_phone ON tickets (plant_id, customer_phone);
CREATE INDEX idx_tickets_status ON tickets (plant_id, status);
CREATE INDEX idx_tickets_code ON tickets (plant_id, code);

-- ======================
-- 6. Cash Movements
-- ======================
CREATE TABLE cash_movements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id        UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    shift_id        UUID NOT NULL REFERENCES shifts(id) ON DELETE CASCADE,

    kind            TEXT NOT NULL
        CHECK (kind IN ('opening_float', 'sale', 'deposit', 'payout', 'count', 'closing')),

    amount_kobo     BIGINT NOT NULL,           -- positive = in, negative = out
    counted_kobo    BIGINT,

    ticket_id       UUID REFERENCES tickets(id),
    recorded_by     UUID REFERENCES users(id),
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_cash_shift ON cash_movements (shift_id, created_at);
CREATE INDEX idx_cash_plant ON cash_movements (plant_id, created_at DESC);

-- ======================
-- 7. Activity Log
-- ======================
CREATE TABLE activity_log (
    id              BIGSERIAL PRIMARY KEY,
    plant_id        UUID REFERENCES plants(id) ON DELETE CASCADE,
    actor_id        UUID REFERENCES users(id),
    action          TEXT NOT NULL,
    subject         TEXT,
    detail          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_plant ON activity_log (plant_id, created_at DESC);

-- ======================
-- Updated_at trigger
-- ======================
CREATE OR REPLACE FUNCTION set_updated_at()
    RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_plants_updated_at
    BEFORE UPDATE ON plants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_shifts_updated_at
    BEFORE UPDATE ON shifts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_tickets_updated_at
    BEFORE UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE plants ENABLE ROW LEVEL SECURITY;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE prices ENABLE ROW LEVEL SECURITY;
ALTER TABLE shifts ENABLE ROW LEVEL SECURITY;
ALTER TABLE tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_movements ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_log ENABLE ROW LEVEL SECURITY;

ALTER TABLE plants FORCE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
ALTER TABLE prices FORCE ROW LEVEL SECURITY;
ALTER TABLE shifts FORCE ROW LEVEL SECURITY;
ALTER TABLE tickets FORCE ROW LEVEL SECURITY;
ALTER TABLE cash_movements FORCE ROW LEVEL SECURITY;
ALTER TABLE activity_log FORCE ROW LEVEL SECURITY;

CREATE OR REPLACE FUNCTION current_plant_id()
    RETURNS UUID AS $$
SELECT NULLIF(current_setting('app.current_plant_id', true), '')::UUID;
$$ LANGUAGE sql STABLE;

-- Plants: a tenant can only see itself
CREATE POLICY plants_isolation ON plants
    USING (id = current_plant_id());

-- Users
CREATE POLICY users_isolation ON users
    USING (plant_id = current_plant_id());

-- Prices
CREATE POLICY prices_isolation ON prices
    USING (plant_id = current_plant_id());

-- Shifts
CREATE POLICY shifts_isolation ON shifts
    USING (plant_id = current_plant_id());

-- Tickets
CREATE POLICY tickets_isolation ON tickets
    USING (plant_id = current_plant_id());

-- Cash movements
CREATE POLICY cash_movements_isolation ON cash_movements
    USING (plant_id = current_plant_id());

-- Activity log
CREATE POLICY activity_log_isolation ON activity_log
    USING (plant_id = current_plant_id());

-- =====================================================
-- 1. Create the two roles
-- =====================================================

-- Restricted role used by the normal application
CREATE ROLE gatstogo_app
    LOGIN
    PASSWORD 'change_this_strong_password_app'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE;

-- Admin role that can bypass Row Level Security
CREATE ROLE gatstogo_admin
    LOGIN
    PASSWORD 'change_this_strong_password_admin'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    BYPASSRLS;          -- This is the key privilege


-- =====================================================
-- 2. Grant schema usage
-- =====================================================
GRANT USAGE ON SCHEMA public TO gatstogo_app;
GRANT USAGE ON SCHEMA public TO gatstogo_admin;


-- =====================================================
-- 3. Grant table permissions
-- =====================================================

-- App role: normal read/write on tenant tables
GRANT SELECT, INSERT, UPDATE, DELETE ON
    plants, users, prices, shifts, tickets,
    cash_movements, activity_log, reserved_slugs
    TO gatstogo_app;

-- Admin role: full control
GRANT ALL PRIVILEGES ON
    plants, users, prices, shifts, tickets,
    cash_movements, activity_log, reserved_slugs
    TO gatstogo_admin;


-- =====================================================
-- 4. Grant sequence permissions (for BIGSERIAL / identity)
-- =====================================================
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO gatstogo_app;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO gatstogo_admin;


-- =====================================================
-- 5. Make sure future tables also get the right permissions
-- =====================================================
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO gatstogo_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT ALL PRIVILEGES ON TABLES TO gatstogo_admin;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO gatstogo_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT ALL PRIVILEGES ON SEQUENCES TO gatstogo_admin;


-- =====================================================
-- 6. (Optional but recommended) Revoke public access
-- =====================================================
REVOKE ALL ON SCHEMA public FROM PUBLIC;

-- Prevent the app role from changing ownership or disabling RLS
REVOKE ALL ON SCHEMA public FROM gatstogo_app;
GRANT USAGE ON SCHEMA public TO gatstogo_app;





