CREATE TABLE plant_reviews (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plant_id       UUID NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
    ticket_id      UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    token          TEXT NOT NULL UNIQUE,
    rating         INTEGER CHECK (rating BETWEEN 1 AND 5),
    note           TEXT,
    requested_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plant_id, ticket_id)
);

CREATE INDEX idx_plant_reviews_plant_submitted ON plant_reviews (plant_id, submitted_at DESC);
CREATE INDEX idx_plant_reviews_ticket ON plant_reviews (plant_id, ticket_id);

CREATE TRIGGER trg_plant_reviews_updated_at
    BEFORE UPDATE ON plant_reviews
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE plant_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE plant_reviews FORCE ROW LEVEL SECURITY;

CREATE POLICY plant_reviews_isolation ON plant_reviews
    USING (plant_id = current_plant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON plant_reviews TO gatstogo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON plant_reviews TO gatstogo_admin;
