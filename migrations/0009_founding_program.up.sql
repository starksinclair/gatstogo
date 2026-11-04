-- Launch program: the first 50 plant applications get a "Founding Plant"
-- badge (1st, 2nd and 3rd get their own), and the first 50 gas buyers who
-- register get the first-order offer. Spots are counted from applications,
-- not approvals, so a slow review never delays or blocks a spot.

-- founding_rank is 1..50 for a self-serve application that claimed a spot,
-- NULL for everything else (admin-created and older plants).
ALTER TABLE plants
    ADD COLUMN founding_rank INTEGER CHECK (founding_rank IS NULL OR founding_rank BETWEEN 1 AND 50);

CREATE UNIQUE INDEX idx_plants_founding_rank ON plants (founding_rank) WHERE founding_rank IS NOT NULL;

-- Gas buyers who registered interest on /get-started. Not tenant data
-- (no plant yet), so no RLS: only the admin role can read or write it.
CREATE TABLE buyer_signups (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    phone       TEXT NOT NULL,
    phone_key   TEXT NOT NULL UNIQUE,
    email       TEXT,
    state       TEXT NOT NULL,
    source      TEXT,
    offer_rank  INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_buyer_signups_state ON buyer_signups (state);

REVOKE ALL ON buyer_signups FROM gatstogo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON buyer_signups TO gatstogo_admin;
