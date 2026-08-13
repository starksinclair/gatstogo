-- Rotates the login passwords for the two roles migration 0001 created
-- (gatstogo_app, gatstogo_admin) away from the hardcoded placeholders
-- ('change_this_strong_password_app' / '_admin') embedded in that
-- historical migration. 0001 itself is left untouched -- already-applied
-- migrations shouldn't be edited in place.
--
-- Expects to be run with psql -v app_password=... -v admin_password=...
-- (see migrations/migrate.sh). The :'var' form is psql's quoted-literal
-- substitution: it safely SQL-quotes whatever value is passed, so this is
-- not vulnerable to SQL injection even if a password contains a quote
-- character.
ALTER ROLE gatstogo_app   WITH PASSWORD :'app_password';
ALTER ROLE gatstogo_admin WITH PASSWORD :'admin_password';
