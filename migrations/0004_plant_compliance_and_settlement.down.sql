ALTER TABLE users
    DROP COLUMN IF EXISTS email;

ALTER TABLE plants
    DROP COLUMN IF EXISTS paystack_subaccount_code,
    DROP COLUMN IF EXISTS bank_account_name,
    DROP COLUMN IF EXISTS bank_account_number,
    DROP COLUMN IF EXISTS bank_name,
    DROP COLUMN IF EXISTS bank_code,
    DROP COLUMN IF EXISTS nmdpra_license_number,
    DROP COLUMN IF EXISTS state,
    DROP COLUMN IF EXISTS cac_number,
    DROP COLUMN IF EXISTS legal_business_name;
