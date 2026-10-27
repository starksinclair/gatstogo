-- ============================================================================
-- Demo data for layout testing: ~1,000 users, 60 plants, 30 days of trade.
-- LOCAL DEV ONLY. Fake people, businesses, and bank details.
--
--   Seed:    docker exec -i gatstogo-postgres psql -U gatstogo -d gatstogo -v ON_ERROR_STOP=1 < scripts/dev/seed-demo-data.sql
--   Remove:  docker exec -i gatstogo-postgres psql -U gatstogo -d gatstogo -v ON_ERROR_STOP=1 < scripts/dev/cleanup-demo-data.sql
--
-- What it adds:
--   * 60 demo plants (slug "demo-..."): 44 active, 8 under review,
--     5 suspended, 3 closed. Deleting them cascades everything they own.
--   * Extra data on the existing "sunrise" plant (the one the dev owner
--     login opens): 40 staff, 5 pumps, price history, hours, 30 days of
--     shifts, tickets, cash, reviews, and activity. Every such row is
--     recorded in zz_seed_registry so cleanup removes exactly those.
--   * Admin activity (plant activations / suspensions / closures).
-- ============================================================================

BEGIN;

DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM plants WHERE slug LIKE 'demo-%') THEN
		RAISE EXCEPTION 'Demo data is already seeded. Run scripts/dev/cleanup-demo-data.sql first.';
	END IF;
	IF NOT EXISTS (SELECT 1 FROM plants WHERE slug = 'sunrise') THEN
		RAISE EXCEPTION 'The sunrise dev plant is missing (seed migration 0002 not applied?).';
	END IF;
END $$;

CREATE TABLE IF NOT EXISTS zz_seed_registry (tbl text NOT NULL, id text NOT NULL);

SELECT setseed(0.4242);

-- ---------------------------------------------------------------------------
-- Random helpers (session-only)
-- ---------------------------------------------------------------------------
CREATE FUNCTION pg_temp.pick(arr text[]) RETURNS text LANGUAGE sql VOLATILE AS
$$ SELECT arr[1 + floor(random() * array_length(arr, 1))::int] $$;

CREATE FUNCTION pg_temp.rint(lo int, hi int) RETURNS int LANGUAGE sql VOLATILE AS
$$ SELECT lo + floor(random() * (hi - lo + 1))::int $$;

CREATE FUNCTION pg_temp.fname() RETURNS text LANGUAGE sql VOLATILE AS $$ SELECT pg_temp.pick(ARRAY[
	'Adaeze','Chinedu','Ngozi','Emeka','Ifeoma','Obinna','Chiamaka','Uchenna','Nkechi','Ikenna',
	'Tunde','Bisi','Kemi','Segun','Funmi','Yemi','Bola','Femi','Toyin','Wale','Seun','Dayo','Lola','Kunle',
	'Aisha','Musa','Fatima','Ibrahim','Zainab','Usman','Hauwa','Abubakar','Maryam','Yusuf','Amina','Sani',
	'Grace','Blessing','Esther','Daniel','Samuel','Joy','Peace','Victor','Faith','Godwin','Mercy','Kelechi',
	'Efe','Osas','Oghenekaro','Ese','Tamuno','Ebi','Ini','Eno','Akpan','Etim','Ifeanyi','Chidi','Somto','Nnamdi'
]) $$;

CREATE FUNCTION pg_temp.lname() RETURNS text LANGUAGE sql VOLATILE AS $$ SELECT pg_temp.pick(ARRAY[
	'Okafor','Eze','Nwosu','Okonkwo','Obi','Nnadi','Chukwu','Okeke','Onyeka','Uzor','Agu','Ibe',
	'Adeyemi','Ogunleye','Balogun','Adebayo','Olawale','Afolabi','Bakare','Ojo','Adewale','Oyelaran','Salami','Akinola',
	'Bello','Abdullahi','Mohammed','Suleiman','Garba','Aliyu','Danjuma','Lawal','Yakubu','Idris',
	'Etim','Bassey','Effiong','Okon','Udo','Edet','Ekanem','Ita',
	'Omoregie','Osagie','Igbinedion','Ighodaro','Oghenekaro','Tobi','Briggs','George','Amadi','Wokoma'
]) $$;

-- Nigerian mobile number; prefix + 7 digits. "seq" keeps numbers unique.
CREATE FUNCTION pg_temp.phone(seq int) RETURNS text LANGUAGE sql VOLATILE AS $$ SELECT
	'0' || pg_temp.pick(ARRAY['803','806','813','816','810','814','703','706','903','906','805','807','815','905','802','808','812','701','708','902','907','901','704'])
	|| lpad((seq % 10000000)::text, 7, '0') $$;

-- A believable time of day for a sale on a given (Lagos) date: 07:00-19:00,
-- busier in the morning and early evening.
CREATE FUNCTION pg_temp.sale_time(d date) RETURNS timestamptz LANGUAGE sql VOLATILE AS $$
	SELECT ((d + time '07:00') + make_interval(mins => (CASE WHEN random() < 0.45
		THEN floor(random() * 240)            -- 07:00-11:00 rush
		ELSE floor(random() * 720) END)::int)
		+ make_interval(secs => floor(random() * 60)::int)) AT TIME ZONE 'Africa/Lagos' $$;

CREATE TEMP TABLE sun AS SELECT id, slug FROM plants WHERE slug = 'sunrise';
CREATE TEMP TABLE sun_owner AS SELECT id FROM users WHERE plant_id = (SELECT id FROM sun) AND role = 'owner' LIMIT 1;
CREATE TEMP TABLE admin_user AS SELECT id FROM users WHERE role = 'admin' LIMIT 1;

-- ---------------------------------------------------------------------------
-- 1. Demo plants
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE places (n int, city text, state text, street text);
INSERT INTO places VALUES
	(1,'Lagos','Lagos','Ikorodu Road, Ketu'), (2,'Lagos','Lagos','Lekki-Epe Expressway, Ajah'), (3,'Ikeja','Lagos','Obafemi Awolowo Way'),
	(4,'Abuja','FCT (Abuja)','Kubwa Express, Kubwa'), (5,'Abuja','FCT (Abuja)','Airport Road, Lugbe'), (6,'Port Harcourt','Rivers','Ikwerre Road, Rumuokoro'),
	(7,'Port Harcourt','Rivers','Trans-Amadi Industrial Layout'), (8,'Ibadan','Oyo','Ring Road, Challenge'), (9,'Ibadan','Oyo','Iwo Road'),
	(10,'Kano','Kano','Zaria Road, Kano'), (11,'Kaduna','Kaduna','Ahmadu Bello Way'), (12,'Enugu','Enugu','Ogui Road, New Haven'),
	(13,'Owerri','Imo','Wetheral Road'), (14,'Benin City','Edo','Sapele Road'), (15,'Warri','Delta','Effurun-Sapele Road'),
	(16,'Uyo','Akwa Ibom','Oron Road'), (17,'Calabar','Cross River','Murtala Mohammed Highway'), (18,'Abeokuta','Ogun','Lafenwa Road'),
	(19,'Onitsha','Anambra','Upper Iweka Road'), (20,'Asaba','Delta','Nnebisi Road'), (21,'Ilorin','Kwara','Taiwo Road'),
	(22,'Jos','Plateau','Tafawa Balewa Street'), (23,'Akure','Ondo','Oyemekun Road'), (24,'Osogbo','Osun','Gbongan Road'),
	(25,'Aba','Abia','Faulks Road'), (26,'Yenagoa','Bayelsa','Mbiama-Yenagoa Road'), (27,'Makurdi','Benue','Old Otukpo Road'),
	(28,'Lokoja','Kogi','Ganaja Road'), (29,'Sagamu','Ogun','Akarigbo Street'), (30,'Nnewi','Anambra','Owerri Road');

CREATE TEMP TABLE demo_plants AS
WITH g AS (
	SELECT n,
		(ARRAY['Royal Flame','Prime Gas','Blue Ridge','Gas Haven','Eko Flame','Northgate','Sterling Cylinder','Crystal Gas',
		       'Bright Star','Unity Gas','Golden Fill','Delta Breeze','Harmony LPG','Trust Flame','Liberty Gas','Oasis Gas',
		       'Summit Energy','Victory Gas','Evergreen LPG','Pinnacle Gas'])[1 + (n - 1) % 20] AS stem,
		(ARRAY[' Gas Plant',' LPG',' Gas',' Energy',' Gas Station'])[1 + ((n - 1) / 20 + n) % 5] AS suffix_raw,
		CASE WHEN n <= 44 THEN 'active' WHEN n <= 52 THEN 'provisioning' WHEN n <= 57 THEN 'suspended' ELSE 'closed' END AS status
	FROM generate_series(1, 60) n
)
SELECT g.n, gen_random_uuid() AS id, g.status,
	g.stem || CASE WHEN g.stem LIKE '% Gas' AND g.suffix_raw LIKE ' Gas%' THEN (ARRAY[' Plant',' LPG',' Depot'])[1 + g.n % 3] ELSE g.suffix_raw END
		|| CASE WHEN g.n > 20 THEN ' ' || pl.city ELSE '' END AS name,
	'demo-' || regexp_replace(lower(g.stem || '-' || pl.city), '[^a-z0-9]+', '-', 'g') || '-' || g.n AS slug,
	pl.city, pl.state,
	pg_temp.rint(1, 98) || ' ' || pl.street AS address,
	CASE g.status
		WHEN 'provisioning' THEN now() - make_interval(hours => pg_temp.rint(2, 200))
		ELSE now() - make_interval(days => pg_temp.rint(45, 420)) END AS created_at,
	-- daily sales volume for active plants (small kiosk to busy depot)
	(ARRAY[8, 12, 18, 25, 35, 50, 70])[1 + (g.n * 3) % 7] AS daily_sales
FROM g JOIN places pl ON pl.n = 1 + (g.n * 7) % 30;

INSERT INTO plants (id, slug, name, city, address, phone, status, created_at, updated_at,
	primary_color, secondary_color, button_color, button_text_color,
	custom_domain, domain_status,
	legal_business_name, cac_number, state, nmdpra_license_number,
	bank_code, bank_name, bank_account_number, bank_account_name, paystack_subaccount_code)
SELECT dp.id, dp.slug, dp.name, dp.city, dp.address, pg_temp.phone(800000 + dp.n), dp.status, dp.created_at, dp.created_at,
	c.primary_color, c.secondary_color, c.primary_color, '#FFFFFF',
	CASE WHEN dp.n % 4 = 0 AND dp.status = 'active' THEN replace(dp.slug, 'demo-', '') || '.com.ng' END,
	CASE WHEN dp.n % 4 = 0 AND dp.status = 'active' THEN (ARRAY['active','active','pending','broken'])[1 + dp.n % 4 + (dp.n / 8) % 2] ELSE 'none' END,
	dp.stem_legal, 'RC' || (1200000 + dp.n * 7919 % 800000), dp.state,
	'NMDPRA/LPG/' || (2021 + dp.n % 5) || '/' || lpad((dp.n * 37)::text, 4, '0'),
	b.code, b.name, lpad((3000000000::bigint + dp.n * 104729)::text, 10, '0'), upper(dp.stem_legal),
	'ACCT_' || substr(md5(dp.slug), 1, 10)
FROM (
	SELECT demo_plants.*, split_part(name, ' ' || city, 1) || (ARRAY[' Limited',' Nigeria Ltd',' Ventures Ltd',' Energy Ltd'])[1 + n % 4] AS stem_legal
	FROM demo_plants
) dp
CROSS JOIN LATERAL (SELECT (ARRAY['#006B4D','#0B5ED7','#B42318','#7A2E8E','#C2410C','#0F766E','#1D4ED8','#15803D'])[1 + dp.n % 8] AS primary_color,
                           (ARRAY['#10B981','#60A5FA','#F97316','#C084FC','#FBBF24','#2DD4BF','#93C5FD','#4ADE80'])[1 + dp.n % 8] AS secondary_color) c
CROSS JOIN LATERAL (SELECT (ARRAY['058','044','057','033','011','035','232','070','50515','999992'])[1 + dp.n % 10] AS code,
                           (ARRAY['GTBank','Access Bank','Zenith Bank','UBA','First Bank','Wema Bank','Sterling Bank','Fidelity Bank','Moniepoint MFB','OPay'])[1 + dp.n % 10] AS name) b;

-- ---------------------------------------------------------------------------
-- 2. Users: one owner per demo plant, staff per plant, 40 extra at sunrise
-- ---------------------------------------------------------------------------
INSERT INTO users (plant_id, name, phone, email, role, active, created_at, updated_at)
SELECT dp.id, f || ' ' || l, pg_temp.phone(500000 + dp.n), lower(f || '.' || l || dp.n) || '@gmail.com', 'owner', true, dp.created_at, dp.created_at
FROM demo_plants dp, LATERAL (SELECT pg_temp.fname() f, pg_temp.lname() l WHERE dp.n IS NOT NULL) nm;

CREATE TEMP TABLE staff_plan AS
SELECT dp.id AS plant_id, dp.n, dp.created_at,
	CASE dp.status WHEN 'active' THEN pg_temp.rint(14, 24) WHEN 'provisioning' THEN 0 ELSE pg_temp.rint(8, 12) END AS staff_count
FROM demo_plants dp;

INSERT INTO users (plant_id, name, phone, role, active, can_edit_price, can_manage_staff, can_manage_pumps, created_at, updated_at)
SELECT sp.plant_id, pg_temp.fname() || ' ' || pg_temp.lname(), pg_temp.phone(sp.n * 1000 + s),
	r.role, random() < 0.93,
	r.role = 'manager' AND random() < 0.6, r.role = 'manager' AND random() < 0.4, r.role = 'manager' AND random() < 0.5,
	t, t
FROM staff_plan sp
CROSS JOIN LATERAL generate_series(1, sp.staff_count) s
CROSS JOIN LATERAL (SELECT CASE WHEN s = 1 THEN 'manager' WHEN random() < 0.08 THEN 'manager' WHEN random() < 0.55 THEN 'cashier' ELSE 'operator' END AS role) r
CROSS JOIN LATERAL (SELECT sp.created_at + (now() - sp.created_at) * random() * 0.8 AS t) tt;

-- sunrise: 40 extra staff (dev owner sees these)
WITH ins AS (
	INSERT INTO users (plant_id, name, phone, role, active, can_edit_price, can_manage_staff, can_manage_pumps, created_at, updated_at)
	SELECT (SELECT id FROM sun), pg_temp.fname() || ' ' || pg_temp.lname(), pg_temp.phone(900000 + s),
		r.role, s > 36 IS NOT TRUE OR random() < 0.5,
		r.role = 'manager' AND s % 2 = 0, r.role = 'manager' AND s % 3 = 0, r.role = 'manager',
		now() - make_interval(days => pg_temp.rint(5, 300)), now()
	FROM generate_series(1, 40) s
	CROSS JOIN LATERAL (SELECT CASE WHEN s <= 3 THEN 'manager' WHEN s % 2 = 0 THEN 'cashier' ELSE 'operator' END AS role) r
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'users', id::text FROM ins;

-- ---------------------------------------------------------------------------
-- 3. Pumps, hours, prices
-- ---------------------------------------------------------------------------
INSERT INTO pumps (plant_id, name, status, sort_order, created_at)
SELECT dp.id, 'Pump ' || p, CASE WHEN random() < 0.85 THEN 'working' WHEN random() < 0.5 THEN 'maintenance' ELSE 'not_working' END, p, dp.created_at
FROM demo_plants dp CROSS JOIN LATERAL generate_series(1, CASE WHEN dp.status = 'provisioning' THEN 1 ELSE pg_temp.rint(2, 5) END) p;

WITH ins AS (
	INSERT INTO pumps (plant_id, name, status, sort_order)
	SELECT (SELECT id FROM sun), 'Pump ' || p, (ARRAY['working','working','working','maintenance','working'])[p - 1], p
	FROM generate_series(2, 6) p
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'pumps', id::text FROM ins;

INSERT INTO plant_hours (plant_id, day_of_week, segment, opens_at, closes_at, closed)
SELECT dp.id, d, 1,
	CASE WHEN d = 0 THEN time '09:00' ELSE time '07:00' END,
	CASE WHEN d = 0 THEN time '16:00' ELSE time '19:00' END,
	d = 0 AND dp.n % 3 = 0
FROM demo_plants dp CROSS JOIN generate_series(0, 6) d;

WITH ins AS (
	INSERT INTO plant_hours (plant_id, day_of_week, segment, opens_at, closes_at, closed)
	SELECT (SELECT id FROM sun), d, 1, CASE WHEN d = 0 THEN time '10:00' ELSE time '07:00' END, CASE WHEN d = 0 THEN time '16:00' ELSE time '19:00' END, false
	FROM generate_series(0, 6) d
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'plant_hours', id::text FROM ins;

-- demo plants: 3-6 price changes over the last 120 days, ending at ₦1,250-₦1,650/kg
INSERT INTO prices (plant_id, price_per_kg, effective_from, set_by, created_at)
SELECT dp.id, (base + k * pg_temp.rint(10, 40)) * 100, t, o.id, t
FROM demo_plants dp
JOIN users o ON o.plant_id = dp.id AND o.role = 'owner'
CROSS JOIN LATERAL (SELECT pg_temp.rint(1150, 1450) AS base, pg_temp.rint(3, 6) AS changes WHERE dp.n IS NOT NULL) pb
CROSS JOIN LATERAL generate_series(0, pb.changes - 1) k
CROSS JOIN LATERAL (SELECT greatest(dp.created_at, now() - make_interval(days => 120 - k * (110 / pb.changes)))
                           + make_interval(hours => pg_temp.rint(0, 10)) AS t) tt;

-- sunrise: price history before today's ₦1,500/kg
WITH ins AS (
	INSERT INTO prices (plant_id, price_per_kg, effective_from, set_by, created_at)
	SELECT (SELECT id FROM sun), v * 100, now() - make_interval(days => d, hours => 4), (SELECT id FROM sun_owner), now() - make_interval(days => d, hours => 4)
	FROM (VALUES (1320, 88), (1350, 70), (1380, 55), (1420, 41), (1450, 28), (1480, 14), (1460, 6)) AS x(v, d)
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'prices', id::text FROM ins;

-- ---------------------------------------------------------------------------
-- 4. Sunrise: 30 days of shifts, tickets, cash, reviews
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE sun_days AS
SELECT ((now() AT TIME ZONE 'Africa/Lagos')::date - d) AS day, d FROM generate_series(0, 29) d;

CREATE TEMP TABLE sun_staff AS
SELECT u.id, u.role, row_number() OVER (PARTITION BY u.role ORDER BY u.id) AS rn
FROM users u JOIN zz_seed_registry r ON r.tbl = 'users' AND r.id = u.id::text
WHERE u.role IN ('cashier','operator') AND u.active;

CREATE TEMP TABLE sun_pumps AS
SELECT id, row_number() OVER (ORDER BY sort_order) AS rn FROM pumps WHERE plant_id = (SELECT id FROM sun) AND status = 'working';

-- Each day: morning (07:00-13:30) and afternoon (13:30-19:00) for a cashier
-- and an operator. Today's afternoon shifts are still open.
CREATE TEMP TABLE sun_shifts AS
SELECT gen_random_uuid() AS id, sd.day, sd.d, slot, rl.role, u.id AS user_id, p.id AS pump_id,
	((sd.day + CASE slot WHEN 1 THEN time '07:00' ELSE time '13:30' END) AT TIME ZONE 'Africa/Lagos') + make_interval(mins => pg_temp.rint(-8, 12)) AS opened_at,
	((sd.day + CASE slot WHEN 1 THEN time '13:30' ELSE time '19:05' END) AT TIME ZONE 'Africa/Lagos') + make_interval(mins => pg_temp.rint(-5, 20)) AS planned_close,
	(pg_temp.rint(15, 40) * 1000)::bigint * 100 AS opening_cash
FROM sun_days sd
CROSS JOIN generate_series(1, 2) slot
CROSS JOIN (VALUES ('cashier'), ('operator')) AS rl(role)
JOIN sun_staff u ON u.role = rl.role AND u.rn = 1 + ((sd.d * 2 + slot) % (SELECT count(*) FROM sun_staff s2 WHERE s2.role = rl.role))
LEFT JOIN sun_pumps p ON rl.role = 'operator' AND p.rn = 1 + ((sd.d + slot) % (SELECT count(*) FROM sun_pumps));

DELETE FROM sun_shifts WHERE opened_at > now();

-- Tickets: ~55-85 a day, fewer on Sundays; today's only up to now.
CREATE TEMP TABLE sun_tix AS
SELECT gen_random_uuid() AS id, sd.day, sd.d, pg_temp.sale_time(sd.day) AS created_at, random() AS r_channel, random() AS r_status, random() AS r_size
FROM sun_days sd
CROSS JOIN LATERAL generate_series(1, CASE WHEN extract(dow FROM sd.day) = 0 THEN pg_temp.rint(25, 40) ELSE pg_temp.rint(55, 85) END) i;

DELETE FROM sun_tix WHERE created_at > now() - interval '2 minutes';

CREATE TEMP TABLE sun_tix2 AS
SELECT t.*,
	pr.id AS price_id, pr.price_per_kg AS rate,
	CASE WHEN t.r_channel < 0.42 THEN 'transfer' WHEN t.r_channel < 0.60 THEN 'terminal' WHEN t.r_channel < 0.70 THEN 'ussd' ELSE 'cash' END AS channel,
	CASE WHEN t.r_size < 0.22 THEN 5000 WHEN t.r_size < 0.42 THEN 12500 WHEN t.r_size < 0.55 THEN 10000 WHEN t.r_size < 0.62 THEN 25000 WHEN t.r_size < 0.66 THEN 50000
	     ELSE pg_temp.rint(3, 30) * 1000 END AS size_grams,
	cs.id AS cashier_shift, cs.user_id AS cashier_id,
	os.id AS operator_shift, os.user_id AS operator_id, os.pump_id
FROM sun_tix t
CROSS JOIN LATERAL (SELECT id, price_per_kg FROM prices WHERE plant_id = (SELECT id FROM sun) AND effective_from <= t.created_at ORDER BY effective_from DESC LIMIT 1) pr0
CROSS JOIN LATERAL (SELECT pr0.id, pr0.price_per_kg) pr
LEFT JOIN LATERAL (SELECT id, user_id FROM sun_shifts s WHERE s.role = 'cashier' AND s.opened_at <= t.created_at ORDER BY s.opened_at DESC LIMIT 1) cs ON true
LEFT JOIN LATERAL (SELECT id, user_id, pump_id FROM sun_shifts s WHERE s.role = 'operator' AND s.opened_at <= t.created_at ORDER BY s.opened_at DESC LIMIT 1) os ON true;

-- prices: before the oldest seeded price there's no row; fall back to the earliest one
UPDATE sun_tix2 SET price_id = (SELECT id FROM prices WHERE plant_id = (SELECT id FROM sun) ORDER BY effective_from LIMIT 1),
	rate = (SELECT price_per_kg FROM prices WHERE plant_id = (SELECT id FROM sun) ORDER BY effective_from LIMIT 1)
WHERE price_id IS NULL;

INSERT INTO shifts (id, plant_id, user_id, device_id, opened_at, closed_at, opening_cash_kobo, closing_cash_kobo, tokens_issued, tokens_returned, notes, pump_id, created_at, updated_at)
SELECT s.id, (SELECT id FROM sun), s.user_id, 'terminal-' || CASE s.role WHEN 'cashier' THEN 'front' ELSE 'pump' END,
	s.opened_at,
	CASE WHEN s.planned_close < now() THEN s.planned_close END,
	s.opening_cash,
	CASE WHEN s.planned_close < now() THEN s.opening_cash + coalesce((SELECT sum(amount_calc) FROM (
		SELECT (x.size_grams::bigint * x.rate / 1000) AS amount_calc FROM sun_tix2 x WHERE x.cashier_shift = s.id AND x.channel = 'cash') q), 0)
		- (CASE WHEN random() < 0.12 THEN pg_temp.rint(1, 20) * 10000 ELSE 0 END) END,
	pg_temp.rint(10, 45),
	CASE WHEN s.planned_close < now() THEN pg_temp.rint(10, 45) END,
	CASE WHEN s.planned_close < now() AND random() < 0.15 THEN pg_temp.pick(ARRAY['Short by small change, customer had no change.','Power outage 30 mins, sales resumed on generator.','Scale recalibrated mid-shift.','Busy shift, two pumps running.']) END,
	s.pump_id, s.opened_at, now()
FROM sun_shifts s;
INSERT INTO zz_seed_registry SELECT 'shifts', id::text FROM sun_shifts;

-- final ticket rows
CREATE TEMP TABLE sun_final AS
SELECT t.*,
	(t.size_grams::bigint * t.rate / 1000) AS amount,
	CASE
		WHEN t.created_at > now() - interval '25 minutes' THEN (ARRAY['pending','paid','paid','dispensing'])[1 + floor(t.r_status * 4)::int]
		WHEN t.d = 0 AND t.created_at > now() - interval '70 minutes' AND t.r_status < 0.3 THEN 'paid'
		WHEN t.r_status < 0.03 THEN 'expired'
		WHEN t.r_status < 0.05 THEN 'cancelled'
		WHEN t.r_status < 0.065 THEN 'voided'
		WHEN t.r_status < 0.075 THEN 'issue'
		ELSE 'filled' END AS status,
	pg_temp.rint(-180, 120) AS fill_delta,
	pg_temp.rint(9500, 15500) AS empty_g,
	pg_temp.rint(1, 4) AS pay_mins,
	pg_temp.rint(6, 45) AS fill_mins,
	pg_temp.fname() || ' ' || pg_temp.lname() AS customer_name,
	pg_temp.phone(pg_temp.rint(1000000, 9999999)) AS customer_phone,
	row_number() OVER () AS rn
FROM sun_tix2 t;

INSERT INTO tickets (id, plant_id, reference, code, customer_name, customer_phone, size_grams, rate_per_kg, price_id, amount, channel, status,
	net_grams, empty_weight_grams, filled_weight_grams, filled_at, filled_shift_id, confirmed_at, paid_at, expires_at,
	sold_by, shift_id, void_reason, voided_by, voided_at, created_at, updated_at, pump_id,
	dispensing_started_at, dispensing_started_by)
SELECT f.id, (SELECT id FROM sun), 'sunrise-' || substr(md5(f.id::text), 1, 12),
	CASE WHEN f.status IN ('pending','paid','dispensing') THEN lpad(((f.rn * 7919) % 10000)::text, 4, '0') ELSE lpad(pg_temp.rint(0, 9999)::text, 4, '0') END,
	CASE WHEN f.channel = 'cash' AND random() < 0.4 THEN NULL ELSE f.customer_name END,
	CASE WHEN f.channel = 'cash' AND random() < 0.5 THEN NULL ELSE f.customer_phone END,
	f.size_grams, f.rate, f.price_id, f.amount, f.channel, f.status,
	CASE WHEN f.status IN ('filled','issue') THEN f.size_grams + f.fill_delta END,
	CASE WHEN f.status IN ('filled','issue') THEN f.empty_g END,
	CASE WHEN f.status IN ('filled','issue') THEN f.empty_g + f.size_grams + f.fill_delta END,
	CASE WHEN f.status IN ('filled','issue') THEN least(now(), f.created_at + make_interval(mins => f.pay_mins + f.fill_mins)) END,
	CASE WHEN f.status IN ('filled','issue') THEN f.operator_shift END,
	CASE WHEN f.status = 'filled' AND random() < 0.35 THEN least(now(), f.created_at + make_interval(mins => f.pay_mins + f.fill_mins + 2)) END,
	CASE WHEN f.status NOT IN ('pending','expired','cancelled') THEN f.created_at + make_interval(mins => CASE WHEN f.channel = 'cash' THEN 0 ELSE f.pay_mins END) END,
	f.created_at + interval '2 hours',
	CASE WHEN f.channel = 'cash' THEN f.cashier_id END,
	CASE WHEN f.channel = 'cash' THEN f.cashier_shift END,
	CASE WHEN f.status = 'voided' THEN pg_temp.pick(ARRAY['Customer changed their mind before fill.','Duplicate payment, refunded.','Wrong amount entered at the counter.']) END,
	CASE WHEN f.status = 'voided' THEN (SELECT id FROM sun_owner) END,
	CASE WHEN f.status = 'voided' THEN f.created_at + interval '20 minutes' END,
	f.created_at, f.created_at,
	CASE WHEN f.status IN ('filled','issue','dispensing') THEN f.pump_id END,
	CASE WHEN f.status IN ('filled','issue','dispensing') THEN least(now(), f.created_at + make_interval(mins => f.pay_mins + 2)) END,
	CASE WHEN f.status IN ('filled','issue','dispensing') THEN f.operator_id END
FROM sun_final f;
INSERT INTO zz_seed_registry SELECT 'tickets', id::text FROM sun_final;

-- cash: opening float + cash sales per cashier shift, closing count when closed
WITH ins AS (
	INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, counted_kobo, ticket_id, recorded_by, note, created_at)
	SELECT (SELECT id FROM sun), s.id, 'opening_float', s.opening_cash, s.opening_cash, NULL, s.user_id, NULL, s.opened_at
	FROM sun_shifts s WHERE s.role = 'cashier'
	UNION ALL
	SELECT (SELECT id FROM sun), f.cashier_shift, 'sale', f.amount, NULL, f.id, f.cashier_id, NULL, f.created_at
	FROM sun_final f WHERE f.channel = 'cash' AND f.cashier_shift IS NOT NULL AND f.status NOT IN ('pending','expired','cancelled')
	UNION ALL
	SELECT (SELECT id FROM sun), s.id, 'deposit', pg_temp.rint(5, 15) * 1000000, NULL, NULL, s.user_id, 'Bank deposit, midday', s.opened_at + interval '3 hours'
	FROM sun_shifts s WHERE s.role = 'cashier' AND s.planned_close < now() AND random() < 0.25
	UNION ALL
	SELECT (SELECT id FROM sun), s.id, 'closing', sh.closing_cash_kobo, sh.closing_cash_kobo, NULL, s.user_id,
		CASE WHEN sh.notes IS NOT NULL THEN sh.notes END, sh.closed_at
	FROM sun_shifts s JOIN shifts sh ON sh.id = s.id WHERE s.role = 'cashier' AND sh.closed_at IS NOT NULL
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'cash_movements', id::text FROM ins;

-- reviews on ~12% of filled tickets, most submitted
WITH picked AS (
	SELECT f.id, f.created_at, random() AS r FROM sun_final f WHERE f.status = 'filled' AND random() < 0.12
), ins AS (
	INSERT INTO plant_reviews (plant_id, ticket_id, token, rating, note, requested_at, submitted_at, created_at)
	SELECT (SELECT id FROM sun), p.id, md5(p.id::text || 'review'),
		CASE WHEN p.r < 0.85 THEN (ARRAY[5,5,5,4,4,4,3,5,4,2])[1 + floor(random() * 10)::int] END,
		CASE WHEN p.r < 0.85 AND random() < 0.6 THEN pg_temp.pick(ARRAY[
			'Fast service, the attendant was polite.','Weight was correct, I checked at home.','Queue was long this morning but moved fast.',
			'Paid online and got filled in 5 minutes. Good.','Please add more pumps on Saturdays.','Very neat place and honest scale.',
			'Took a while to find my code but staff helped.','Best price in this area.','Card terminal was not working, had to transfer.',
			'Excellent! Will come back.','Cylinder was filled exactly as paid.','Attendant was rude today.'
		]) END,
		p.created_at + interval '30 minutes',
		CASE WHEN p.r < 0.85 THEN least(now(), p.created_at + make_interval(hours => pg_temp.rint(1, 30))) END,
		p.created_at + interval '30 minutes'
	FROM picked p
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'plant_reviews', id::text FROM ins;

-- ---------------------------------------------------------------------------
-- 5. Demo plants: 30 days of sales (lighter detail; no shifts)
-- ---------------------------------------------------------------------------
INSERT INTO tickets (plant_id, reference, code, customer_name, customer_phone, size_grams, rate_per_kg, price_id, amount, channel, status,
	net_grams, filled_at, paid_at, created_at, updated_at)
SELECT x.plant_id, x.slug || '-' || substr(md5(x.plant_id::text || x.k || x.created_at::text), 1, 12), lpad(pg_temp.rint(0, 9999)::text, 4, '0'),
	pg_temp.fname() || ' ' || pg_temp.lname(), pg_temp.phone(pg_temp.rint(1000000, 9999999)),
	x.size_grams, x.rate, x.price_id, (x.size_grams::bigint * x.rate / 1000),
	x.channel, x.status,
	CASE WHEN x.status = 'filled' THEN x.size_grams + pg_temp.rint(-150, 100) END,
	CASE WHEN x.status = 'filled' THEN least(now(), x.created_at + make_interval(mins => pg_temp.rint(8, 40))) END,
	CASE WHEN x.status IN ('filled','paid') THEN x.created_at + make_interval(mins => pg_temp.rint(0, 4)) END,
	x.created_at, x.created_at
FROM (
	SELECT dp.id AS plant_id, dp.slug, k, ts.created_at,
		pr.id AS price_id, pr.price_per_kg AS rate,
		(ARRAY['transfer','transfer','transfer','terminal','ussd','cash','cash'])[1 + floor(random() * 7)::int] AS channel,
		-- no open (pending/paid) demo tickets: their 4-digit codes must be
		-- unique per plant, and random ones could collide
		CASE WHEN random() < 0.05 THEN 'expired' ELSE 'filled' END AS status,
		(ARRAY[5000, 5000, 10000, 12500, 12500, 12500, 25000, 6000, 8000, 15000])[1 + floor(random() * 10)::int] AS size_grams
	FROM demo_plants dp
	CROSS JOIN LATERAL generate_series(0, 29) d
	CROSS JOIN LATERAL generate_series(1, greatest(1, (dp.daily_sales * (0.6 + random() * 0.8))::int)) k
	CROSS JOIN LATERAL (SELECT pg_temp.sale_time((now() AT TIME ZONE 'Africa/Lagos')::date - d) AS created_at WHERE k IS NOT NULL AND dp.n IS NOT NULL) ts
	CROSS JOIN LATERAL (SELECT id, price_per_kg FROM prices p WHERE p.plant_id = dp.id ORDER BY (p.effective_from <= ts.created_at) DESC, p.effective_from DESC LIMIT 1) pr
	WHERE (dp.status = 'active' AND ts.created_at < now() - interval '2 minutes')
	   OR (dp.status = 'suspended' AND d > 12 + dp.n % 10)
	   OR (dp.status = 'closed' AND d > 25)
) x;

-- ---------------------------------------------------------------------------
-- 6. Activity
-- ---------------------------------------------------------------------------
-- admin: status changes (activation; then suspension/closure where relevant)
INSERT INTO activity_log (plant_id, actor_id, action, subject, detail, created_at)
SELECT dp.id, (SELECT id FROM admin_user), 'plant.status_changed', 'active', '{}'::jsonb, dp.created_at + make_interval(hours => pg_temp.rint(3, 60))
FROM demo_plants dp WHERE dp.status IN ('active','suspended','closed')
UNION ALL
SELECT dp.id, (SELECT id FROM admin_user), 'plant.status_changed', 'suspended', '{}'::jsonb, now() - make_interval(days => 12 + dp.n % 10, hours => pg_temp.rint(1, 8))
FROM demo_plants dp WHERE dp.status IN ('suspended','closed')
UNION ALL
SELECT dp.id, (SELECT id FROM admin_user), 'plant.status_changed', 'closed', '{}'::jsonb, now() - make_interval(days => 3 + dp.n % 4, hours => pg_temp.rint(1, 8))
FROM demo_plants dp WHERE dp.status = 'closed'
UNION ALL
SELECT dp.id, (SELECT id FROM admin_user), 'plant.status_changed', 'active', '{}'::jsonb, now() - make_interval(hours => pg_temp.rint(2, 30))
FROM demo_plants dp WHERE dp.status = 'active' AND dp.n IN (3, 17, 29);

-- sunrise: shifts, price changes, staff additions, voids, reviews
WITH ins AS (
	INSERT INTO activity_log (plant_id, actor_id, action, subject, detail, created_at)
	SELECT (SELECT id FROM sun), s.user_id, 'shift.opened', s.id::text, '{}'::jsonb, s.opened_at FROM sun_shifts s
	UNION ALL
	SELECT (SELECT id FROM sun), sh.user_id, 'shift.closed', sh.id::text, '{}'::jsonb, sh.closed_at
	FROM shifts sh JOIN sun_shifts s ON s.id = sh.id WHERE sh.closed_at IS NOT NULL
	UNION ALL
	SELECT (SELECT id FROM sun), (SELECT id FROM sun_owner), 'price.set', p.id::text, jsonb_build_object('price_per_kg_kobo', p.price_per_kg), p.effective_from
	FROM prices p JOIN zz_seed_registry r ON r.tbl = 'prices' AND r.id = p.id::text
	UNION ALL
	SELECT (SELECT id FROM sun), (SELECT id FROM sun_owner), 'staff.created', u.id::text, jsonb_build_object('role', u.role), u.created_at
	FROM users u JOIN zz_seed_registry r ON r.tbl = 'users' AND r.id = u.id::text
	UNION ALL
	SELECT (SELECT id FROM sun), (SELECT id FROM sun_owner), 'ticket.voided', t.reference, jsonb_build_object('reason', t.void_reason), t.voided_at
	FROM tickets t JOIN zz_seed_registry r ON r.tbl = 'tickets' AND r.id = t.id::text WHERE t.status = 'voided'
	RETURNING id
)
INSERT INTO zz_seed_registry SELECT 'activity_log', id::text FROM ins;

COMMIT;

-- ---------------------------------------------------------------------------
-- Summary
-- ---------------------------------------------------------------------------
SELECT 'plants (all)' AS what, count(*)::text AS how_many FROM plants
UNION ALL SELECT 'users (all)', count(*)::text FROM users
UNION ALL SELECT 'tickets (all)', count(*)::text FROM tickets
UNION ALL SELECT 'sunrise staff', count(*)::text FROM users WHERE plant_id = (SELECT id FROM plants WHERE slug = 'sunrise')
UNION ALL SELECT 'sunrise tickets', count(*)::text FROM tickets WHERE plant_id = (SELECT id FROM plants WHERE slug = 'sunrise')
UNION ALL SELECT 'sunrise tickets today', count(*)::text FROM tickets WHERE plant_id = (SELECT id FROM plants WHERE slug = 'sunrise') AND created_at >= date_trunc('day', now())
UNION ALL SELECT 'shifts (all)', count(*)::text FROM shifts
UNION ALL SELECT 'reviews (all)', count(*)::text FROM plant_reviews
UNION ALL SELECT 'admin activity entries', count(*)::text FROM activity_log al JOIN users u ON u.id = al.actor_id AND u.role = 'admin';
