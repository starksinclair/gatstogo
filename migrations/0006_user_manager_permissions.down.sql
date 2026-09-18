ALTER TABLE users
    DROP COLUMN IF EXISTS can_manage_staff,
    DROP COLUMN IF EXISTS can_edit_price;
