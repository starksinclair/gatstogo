-- Adds the business/regulatory identity and real-settlement fields plant
-- onboarding was missing (see internal/plants/plants.go's CreateParams and
-- Create for how these are validated and written, and internal/payments/
-- paystack.go for the Paystack Subaccount fields specifically). Purely
-- additive and nullable -- every existing seeded/test plant keeps working
-- unchanged, just reading back as "not yet provided" on these columns until
-- someone fills them in.
ALTER TABLE plants
    ADD COLUMN legal_business_name      TEXT,
    ADD COLUMN cac_number               TEXT,
    ADD COLUMN state                    TEXT,
    ADD COLUMN nmdpra_license_number    TEXT,
    ADD COLUMN bank_code                TEXT,
    ADD COLUMN bank_name                TEXT,
    ADD COLUMN bank_account_number      TEXT,
    ADD COLUMN bank_account_name        TEXT,
    ADD COLUMN paystack_subaccount_code TEXT;

-- The owner's email -- unlocks a real password-reset flow later (not built
-- this round, just the column and the now-required onboarding field).
ALTER TABLE users
    ADD COLUMN email TEXT;
