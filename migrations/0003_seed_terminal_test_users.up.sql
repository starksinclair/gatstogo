-- Real bcrypt PIN hashes for local/dev testing of the staff terminal.
-- The placeholder hashes from 0002_seed_data.up.sql can't actually be
-- logged in with. Test PINs (plaintext, dev-only):
--   Mary Attendant (cashier) -> 4242
--   James Manager  (manager) -> 1357
--   Tunde Operator (operator) -> 9081

UPDATE users
SET pin_hash = '$2a$10$nrnKmtu8Fqk0UKTZ2Q3to.a51r3ag9U9QmZx.L8D2ALueOlPOkPiq' -- 4242
WHERE id = '33333333-3333-3333-3333-333333333333';

INSERT INTO users (id, plant_id, name, phone, role, pin_hash, active)
VALUES (
           '44444444-4444-4444-4444-444444444444',
           '11111111-1111-1111-1111-111111111111',
           'James Manager',
           '08011122233',
           'manager',
           '$2a$10$Z7B27HyN3Mm1IMtpbt4BG.JdouIIvrJL.AtNwHwcbpJgia2JhGwDq', -- 1357
           true
       );

INSERT INTO users (id, plant_id, name, phone, role, pin_hash, active)
VALUES (
           '55555555-5555-5555-5555-555555555555',
           '11111111-1111-1111-1111-111111111111',
           'Tunde Operator',
           '08044455566',
           'operator',
           '$2a$10$vkkSA29ro76SYDOyMUM7x.EBjRui9JYBISAa4Lpj2fwL4J0e9FJmG', -- 9081
           true
       );

-- A paid, unfilled ticket so the Fill tab has something to redeem.
-- Code: 2714
INSERT INTO tickets (
    plant_id, reference, code, size_grams, rate_per_kg, price_id,
    amount, channel, status, paid_at
)
SELECT
    '11111111-1111-1111-1111-111111111111',
    'seed-ticket-2714',
    '2714',
    12500,
    p.price_per_kg,
    p.id,
    (12500 * p.price_per_kg) / 1000,
    'transfer',
    'paid',
    now()
FROM prices p
WHERE p.plant_id = '11111111-1111-1111-1111-111111111111'
ORDER BY p.effective_from DESC
LIMIT 1;
