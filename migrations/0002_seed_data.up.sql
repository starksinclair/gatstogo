-- Local-dev/demo fixture data only -- never run this against a real
-- production database (real plants are onboarded for real through
-- POST /admin/plants, see internal/plants). The login credentials below
-- are intentionally known/documented, not secret:
--   Owner login   (http://sunrise.localhost:8080/owner/login)
--     phone: 08012345678   password: sunrise-owner-dev
--   Cashier PIN login (http://sunrise.localhost:8080/terminal)
--     phone: 08098765432   PIN: 1234
-- The hashes below are real bcrypt hashes of those two values (generated
-- with golang.org/x/crypto/bcrypt, DefaultCost -- see internal/auth), not
-- placeholders -- this seed data was previously unusable for actually
-- logging in (password_hash/pin_hash were literal placeholder strings
-- that could never match anything typed at the login form), caught by
-- running the app against a live database for the first time.

-- Insert a test plant
INSERT INTO plants (id, slug, name, city, status, primary_color, secondary_color, button_color)
VALUES (
           '11111111-1111-1111-1111-111111111111',
           'sunrise',
           'Sunrise Gas Plant',
           'Owerri',
           'active',
           '#2563eb',
           '#1e40af',
           '#3b82f6'
       );

-- Insert an owner
INSERT INTO users (id, plant_id, name, phone, role, password_hash, active)
VALUES (
           '22222222-2222-2222-2222-222222222222',
           '11111111-1111-1111-1111-111111111111',
           'John Owner',
           '08012345678',
           'owner',
           '$2a$10$83reOOpyZciGHHpEPHmLfeiApGU4pPcifVO2MxXAxYHjmR4O4B7he', -- "sunrise-owner-dev"
           true
       );

-- Insert an attendant
INSERT INTO users (id, plant_id, name, phone, role, pin_hash, active)
VALUES (
           '33333333-3333-3333-3333-333333333333',
           '11111111-1111-1111-1111-111111111111',
           'Mary Attendant',
           '08098765432',
           'cashier',
           '$2a$10$TduoJx1ueqG/UMhrQ4Opnu7o4jGlkoaJu2oZFgHhLdpiTmWnqZmzm', -- "1234"
           true
       );

-- Insert a price
INSERT INTO prices (plant_id, price_per_kg, effective_from)
VALUES (
           '11111111-1111-1111-1111-111111111111',
           150000, -- ₦1,500.00 in kobo
           now()
       );