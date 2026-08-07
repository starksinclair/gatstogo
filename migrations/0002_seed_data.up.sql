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
           '$2a$10$examplehashedpassword', -- replace with real bcrypt hash later
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
           '$2a$10$examplehashedpin',
           true
       );

-- Insert a price
INSERT INTO prices (plant_id, price_per_kg, effective_from)
VALUES (
           '11111111-1111-1111-1111-111111111111',
           150000, -- ₦1,500.00 in kobo
           now()
       );