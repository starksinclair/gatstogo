package pages

// Help centre content. Every answer describes how the product actually
// works today (checked against the code when written); anything set by an
// individual plant (refunds, delivery, hours) sends customers to that
// plant. To add an article, append it to a category's Articles below --
// the hub, category pages, search, and article pages all read from here.

type HelpBlock struct {
	Kind  string   // "p" paragraph, "h" subheading, "steps" numbered list, "list" bullets, "note" tip box
	Text  string   // for p, h, note
	Items []string // for steps, list
}

type HelpArticle struct {
	Slug    string
	Title   string
	Summary string
	Popular bool
	Body    []HelpBlock
}

type HelpCategory struct {
	Slug        string
	Title       string
	Description string
	Icon        string
	Audience    string // "owners" or "customers"
	Articles    []HelpArticle
}

func p(text string) HelpBlock           { return HelpBlock{Kind: "p", Text: text} }
func h(text string) HelpBlock           { return HelpBlock{Kind: "h", Text: text} }
func note(text string) HelpBlock        { return HelpBlock{Kind: "note", Text: text} }
func steps(items ...string) HelpBlock   { return HelpBlock{Kind: "steps", Items: items} }
func bullets(items ...string) HelpBlock { return HelpBlock{Kind: "list", Items: items} }

var HelpCategories = []HelpCategory{
	{
		Slug: "getting-started", Title: "Getting started", Icon: "storefront", Audience: "owners",
		Description: "Apply, get approved, and make your first sale.",
		Articles: []HelpArticle{
			{Slug: "how-to-apply", Title: "How to apply to GatsToGo", Popular: true,
				Summary: "Apply online in about 5 minutes. We review every application.",
				Body: []HelpBlock{
					p("Any licensed LPG plant in Nigeria can apply. The application takes about 5 minutes and is free."),
					steps(
						"Go to Get started from the top of any GatsToGo page.",
						"Enter your plant details: name, web address, city, phone, address, and your starting price per kg.",
						"Enter your business details: legal business name, CAC number, state, and NMDPRA licence number.",
						"Choose your bank and enter your account number. We check it with your bank and show the account name.",
						"Create your owner login: your name, phone number, email, and a password of at least 8 characters.",
						"Agree to the Terms of Service and Privacy Policy, then submit.",
					),
					note("Have your CAC number, NMDPRA Licence to Operate, and business bank account details ready before you start."),
				}},
			{Slug: "what-you-need", Title: "What you need before you apply",
				Summary: "Your CAC number, NMDPRA licence, and a business bank account.",
				Body: []HelpBlock{
					bullets(
						"CAC registration number: RC for a registered company, or BN for a business name.",
						"NMDPRA licence number: as shown on your Licence to Operate.",
						"A business bank account: customer payments settle here. The account name should match your business.",
						"A phone number and email for your owner login and updates.",
					),
					p("You can also choose your brand colors during the application, or leave them for later."),
				}},
			{Slug: "application-review", Title: "What happens after you apply", Popular: true,
				Summary: "We verify your details, then contact you by email and SMS.",
				Body: []HelpBlock{
					p("Every application is reviewed by our team before it goes live. Until then, your page shows customers that the plant is under review, and nobody can buy from it yet."),
					steps(
						"We check your CAC registration, NMDPRA licence, and that the bank account belongs to your plant.",
						"Once approved, we send you an email and an SMS with your login details.",
						"Your customer page goes live and you can start selling.",
					),
				}},
			{Slug: "choosing-web-address", Title: "Choosing your web address",
				Summary: "Your page lives at yourname.gatstogo.ng. Pick something customers recognise.",
				Body: []HelpBlock{
					p("Your web address is the name before .gatstogo.ng, for example sunrise.gatstogo.ng. Customers buy gas there, and your owner and staff logins live there too."),
					bullets(
						"Use lowercase letters, numbers, and hyphens only.",
						"It can't start or end with a hyphen.",
						"Some words are reserved for GatsToGo, such as admin and help.",
						"Each web address can only be used by one plant.",
					),
					note("Pick a short version of your plant's name that customers will recognise on a receipt or WhatsApp message."),
				}},
			{Slug: "first-sale-checklist", Title: "Checklist for your first day",
				Summary: "Set up staff, pumps, hours, and price before customers arrive.",
				Body: []HelpBlock{
					steps(
						"Sign in to your owner dashboard.",
						"Check your gas price in Settings.",
						"Add your pumps and opening hours in Operations.",
						"Add your cashiers and operators in Staff, each with their own 4-digit PIN.",
						"Open your plant's terminal on the counter device and start a test shift.",
						"Share your customer page link with your customers.",
					),
				}},
		},
	},
	{
		Slug: "signing-in", Title: "Signing in and accounts", Icon: "lock", Audience: "owners",
		Description: "Owner logins, passwords, and staff PINs.",
		Articles: []HelpArticle{
			{Slug: "owner-sign-in", Title: "How to sign in as an owner", Popular: true,
				Summary: "Sign in on your plant's own login page with your phone number and password.",
				Body: []HelpBlock{
					p("Each plant has its own login page: your web address followed by /owner/login, for example sunrise.gatstogo.ng/owner/login."),
					steps(
						"Open your plant's login page.",
						"Enter the phone number you registered with.",
						"Enter your password and choose Sign in.",
					),
					note("Lost the link? Use Owner sign in in the GatsToGo footer, type your web address, and we'll take you to your login page."),
				}},
			{Slug: "forgot-password", Title: "I forgot my password", Popular: true,
				Summary: "Reset it from your login page. The link is sent to your email.",
				Body: []HelpBlock{
					steps(
						"On your plant's login page, choose Forgot your password.",
						"Enter your phone number.",
						"Open the email we send to the address on file and follow the link.",
						"Choose a new password.",
					),
					note("Reset links work once and expire after an hour. If yours has expired, just request a new one."),
				}},
			{Slug: "change-password", Title: "Changing your password",
				Summary: "Update it any time from Settings, under Security.",
				Body: []HelpBlock{
					steps(
						"In your dashboard, open Settings.",
						"Under Security, choose Change password.",
						"Enter your current password, then your new password twice.",
						"Choose Update password.",
					),
					bullets(
						"Your new password needs at least 8 characters.",
						"It must be different from your current password.",
					),
				}},
			{Slug: "managers-sign-in", Title: "Can managers use the owner dashboard?",
				Summary: "Yes. Managers sign in on the same login page, with the access you allow.",
				Body: []HelpBlock{
					p("Managers sign in on your plant's owner login page. By default they can see the dashboard. As the owner, you decide whether each manager can also edit the gas price, manage staff, or manage pumps and opening hours."),
					p("Set this per manager in Staff, using Manage in the Permissions column."),
				}},
			{Slug: "staff-pin", Title: "How staff sign in with a PIN",
				Summary: "Staff pick their name on the terminal and enter their 4-digit PIN.",
				Body: []HelpBlock{
					p("Staff don't use passwords. On your plant's terminal (your web address followed by /terminal), each person taps their name and enters their own 4-digit PIN to start a shift."),
					bullets(
						"Each staff member at a plant needs a different PIN.",
						"After 5 wrong PIN attempts, that person is locked out for 20 seconds.",
						"Operators must also choose the pump they're working on.",
					),
				}},
			{Slug: "find-my-login", Title: "I can't find my plant's login page",
				Summary: "Use Owner sign in in the footer, or check your approval email.",
				Body: []HelpBlock{
					p("Your login page address is in the approval email and SMS we sent when your plant went live."),
					p("You can also use Owner sign in at the bottom of any GatsToGo page: type your plant's web address and we'll take you straight to your login page."),
				}},
		},
	},
	{
		Slug: "sales-payments", Title: "Sales and payments", Icon: "receipt", Audience: "owners",
		Description: "How customers pay, codes, cash sales, and voids.",
		Articles: []HelpArticle{
			{Slug: "how-customers-pay", Title: "How customers pay online", Popular: true,
				Summary: "By bank transfer, card, or USSD, processed securely by Paystack.",
				Body: []HelpBlock{
					p("Customers open your page, enter how much they want to spend, their name and phone number, and pay by bank transfer, card, or USSD. Payments are processed by Paystack."),
					p("Once paid, the customer sees a 4-digit code and how many kg they'll get. They show the code at your counter."),
					note("Card and bank details are handled by Paystack. GatsToGo never sees or stores them."),
				}},
			{Slug: "redeeming-codes", Title: "Redeeming a customer's code",
				Summary: "Enter the code on the terminal, then fill and confirm.",
				Body: []HelpBlock{
					steps(
						"The customer shows or reads out their 4-digit code.",
						"On the terminal, enter the code. You'll see the amount paid and the kg to fill.",
						"Fill the cylinder and confirm the fill on the terminal.",
					),
					p("When dispensing starts, the customer gets an SMS confirming their code and the pump being used."),
				}},
			{Slug: "cash-sales", Title: "Recording cash sales",
				Summary: "Record walk-in cash sales on the terminal. No fee is charged.",
				Body: []HelpBlock{
					p("Customers who pay cash at the counter are recorded on the terminal as a cash sale. The cash is added to the cashier's drawer for that shift."),
					note("GatsToGo charges nothing on cash sales. They don't go through Paystack."),
				}},
			{Slug: "ticket-statuses", Title: "What each order status means",
				Summary: "Pending, paid, dispensing, filled, and the rest explained.",
				Body: []HelpBlock{
					bullets(
						"Pending: the customer started paying but payment isn't confirmed yet.",
						"Paid: payment confirmed; the code is ready to redeem.",
						"Dispensing: the fill has started at a pump.",
						"Filled: the cylinder was filled and confirmed.",
						"Issue: the fill was flagged for a problem.",
						"Expired or cancelled: payment wasn't completed.",
						"Voided: an owner or manager cancelled the order.",
					),
				}},
			{Slug: "void-order", Title: "Voiding an order",
				Summary: "Owners and managers can void an order with a reason.",
				Body: []HelpBlock{
					p("If an order was made by mistake, an owner or manager can void it from the dashboard. You'll be asked for a reason, which is kept with the order and in your activity log."),
					note("Voiding an order doesn't refund the customer automatically. Handle refunds directly with your customer."),
				}},
			{Slug: "paid-vs-filled", Title: "Gas paid for versus gas filled",
				Summary: "Today's control shows if you filled more or less than was paid for.",
				Body: []HelpBlock{
					p("On your Overview, Today's control compares the kg customers paid for today with the kg your staff recorded as filled. The difference shows in kg and in naira at your current price."),
					p("A negative difference means less gas was filled than paid for so far, usually because some paid orders haven't been filled yet. A large positive difference is worth checking with your staff."),
				}},
		},
	},
	{
		Slug: "payouts-pricing", Title: "Payouts and pricing", Icon: "bank", Audience: "owners",
		Description: "Fees, settlements, bank details, and your gas price.",
		Articles: []HelpArticle{
			{Slug: "fees", Title: "How much GatsToGo costs", Popular: true,
				Summary: "No setup or monthly fee. 1.5% per online sale. Cash sales are free.",
				Body: []HelpBlock{
					bullets(
						"Setup: free.",
						"Monthly fee: none.",
						"Online sales: GatsToGo keeps 1.5% of each sale.",
						"Cash sales: free.",
						"Staff, pumps, and sales: unlimited.",
					),
					p("Paystack's own processing fee also comes out of your share: 1.5% + ₦100 per payment, capped at ₦2,000, with the ₦100 waived on payments under ₦2,500. The Pricing page has a calculator."),
				}},
			{Slug: "when-paid", Title: "When do I get paid?", Popular: true,
				Summary: "Paystack settles your share straight to your bank account.",
				Body: []HelpBlock{
					p("When a customer pays online, Paystack splits the payment immediately: 98.5% for your plant and 1.5% for GatsToGo. Your share, minus Paystack's fee, settles straight into the bank account you registered, on Paystack's normal settlement schedule."),
					note("GatsToGo never holds your money, and there's nothing to withdraw."),
				}},
			{Slug: "change-bank", Title: "Changing your bank account",
				Summary: "Bank and business details are locked. Contact us to change them.",
				Body: []HelpBlock{
					p("Your settlement account decides where your money goes, so it's locked to protect you. To change it, contact us. We'll verify the request and the new account before updating it."),
				}},
			{Slug: "change-price", Title: "Changing your gas price",
				Summary: "Set a new price per kg in Settings. It applies immediately.",
				Body: []HelpBlock{
					steps(
						"In your dashboard, open Settings.",
						"Under Gas price, enter the new price per kg.",
						"Choose Save price.",
					),
					p("The new price applies to new orders straight away. Orders already paid keep the price they were bought at. Every change is kept in your price history."),
					note("Managers can change the price only if you've given them that permission."),
				}},
			{Slug: "price-history", Title: "Viewing your price history",
				Summary: "Every price change is recorded with who set it and when.",
				Body: []HelpBlock{
					p("Settings shows your price history: each price, when it took effect, and who set it. Prices are never overwritten, so you always have a record."),
				}},
		},
	},
	{
		Slug: "staff-shifts", Title: "Staff and shifts", Icon: "users", Audience: "owners",
		Description: "Add staff, set permissions, and run shifts.",
		Articles: []HelpArticle{
			{Slug: "add-staff", Title: "Adding a staff member", Popular: true,
				Summary: "Add staff in the Staff tab with their name, phone, role, and PIN.",
				Body: []HelpBlock{
					steps(
						"Open Staff in your dashboard.",
						"Under Add staff, enter their name and phone number.",
						"Choose a role: cashier, operator, or manager.",
						"Give them a 4-digit PIN that no one else at your plant uses.",
						"Choose Create staff.",
					),
				}},
			{Slug: "staff-roles", Title: "Staff roles explained",
				Summary: "Cashiers, operators, and managers do different jobs.",
				Body: []HelpBlock{
					bullets(
						"Cashier: works the terminal, redeems codes, and records cash sales.",
						"Operator: works a pump and must choose it when starting a shift.",
						"Manager: uses the terminal and can sign in to the owner dashboard, with the permissions you allow.",
					),
				}},
			{Slug: "manager-permissions", Title: "Giving managers permissions",
				Summary: "Let a manager edit price, manage staff, or manage pumps.",
				Body: []HelpBlock{
					p("In Staff, find the manager and choose Manage under Permissions. Tick what they're allowed to do:"),
					bullets(
						"Edit gas price.",
						"Manage staff: add staff and deactivate terminal access.",
						"Manage pumps: rename pumps, change pump status, and update opening hours.",
					),
					p("Only the owner can change a manager's permissions."),
				}},
			{Slug: "deactivate-staff", Title: "Deactivating a staff member",
				Summary: "Stop someone signing in, without losing their history.",
				Body: []HelpBlock{
					p("In Staff, choose Deactivate next to the person. They can't sign in or use the terminal until you choose Activate again. Their past sales and shifts stay on record."),
				}},
			{Slug: "running-shift", Title: "Starting and ending a shift",
				Summary: "Count opening cash to start, count closing cash to end.",
				Body: []HelpBlock{
					steps(
						"On the terminal, tap your name.",
						"Operators choose their pump.",
						"Enter your PIN and the opening cash in your drawer.",
						"Choose Start shift.",
						"At the end of your shift, count the cash, enter it, add a note if needed, and end the shift.",
					),
					p("Owners see each shift's opening and closing cash in Operations."),
				}},
			{Slug: "cash-drawer", Title: "Reading the cash drawer",
				Summary: "Opening floats, cash sales, and closing counts per shift.",
				Body: []HelpBlock{
					p("Operations shows every cash movement: the opening float when a shift starts, each cash sale, deposits, and the closing count. Compare the closing count with opening cash plus cash sales to spot shortages."),
				}},
		},
	},
	{
		Slug: "plant-setup", Title: "Plant setup", Icon: "gear", Audience: "owners",
		Description: "Pumps, opening hours, branding, and plant details.",
		Articles: []HelpArticle{
			{Slug: "pumps", Title: "Managing pumps",
				Summary: "Add pumps and mark them working, not working, or under maintenance.",
				Body: []HelpBlock{
					p("In Operations, under Pumps, add each pump with a name and order. Set its status:"),
					bullets("Working: available for shifts.", "Not working or under maintenance: operators can't choose it on the terminal."),
				}},
			{Slug: "opening-hours", Title: "Setting opening hours",
				Summary: "Set up to two time blocks per day, or mark a day closed.",
				Body: []HelpBlock{
					p("In Operations, under Opening hours, set when you open and close each day. You can add a second block for a midday break, or tick Closed for days you don't open."),
					p("When your plant is closed, your customer page shows that you're closed and online payment is paused until you reopen."),
				}},
			{Slug: "branding", Title: "Adding your colors and logo",
				Summary: "Brand your customer page from Settings, under Brand kit.",
				Body: []HelpBlock{
					steps(
						"Open Settings, then Brand kit.",
						"Choose your primary, button, secondary, and button text colors.",
						"Upload your logo: PNG, JPEG, or WebP, under 2MB.",
						"Choose Save changes.",
					),
					p("Your colors and logo appear on your customer page and receipts screens."),
				}},
			{Slug: "plant-details", Title: "Updating plant name, address, or phone",
				Summary: "Edit customer-facing details any time in Settings.",
				Body: []HelpBlock{
					p("In Settings, under Plant details, update your plant name, city, phone, and address. These appear on your customer page. Your phone number is how customers contact you about orders."),
					note("Business records like your CAC number, NMDPRA licence, and bank account are locked. Contact us to change them."),
				}},
		},
	},
	{
		Slug: "buying-gas", Title: "Buying gas", Icon: "gas-pump", Audience: "customers",
		Description: "For customers: paying, codes, and receipts.",
		Articles: []HelpArticle{
			{Slug: "how-to-buy", Title: "How to buy gas online", Popular: true,
				Summary: "Enter an amount, pay, and get a 4-digit code for the counter.",
				Body: []HelpBlock{
					steps(
						"Open your plant's GatsToGo page.",
						"Enter how much you want to spend, or tap a popular size. You'll see how many kg you'll get.",
						"Enter your name and phone number.",
						"Choose bank transfer, card, or USSD, and pay.",
						"Note your 4-digit code. Show it at the plant to get your gas.",
					),
					note("No app or account needed."),
				}},
			{Slug: "using-code", Title: "Using your code at the plant",
				Summary: "Show or read out your code. The attendant fills what you paid for.",
				Body: []HelpBlock{
					p("At the plant, show or read your 4-digit code to the attendant. They enter it on their terminal, which tells them exactly how much gas you paid for."),
					p("When filling starts, you'll get an SMS confirming your code and which pump is being used."),
				}},
			{Slug: "lost-code", Title: "I lost my code", Popular: true,
				Summary: "Get it back from Receipts with the phone number you paid with.",
				Body: []HelpBlock{
					steps(
						"Open your plant's GatsToGo page and choose Receipts.",
						"Enter the phone number you used when paying.",
						"Enter the one-time code we send you by SMS.",
						"You'll see your orders and codes.",
					),
				}},
			{Slug: "confirm-received", Title: "Confirming you received your gas",
				Summary: "Confirm from Receipts once your cylinder is filled.",
				Body: []HelpBlock{
					p("After your cylinder is filled, you can confirm you received it from Receipts. This helps the plant keep accurate records."),
				}},
			{Slug: "plant-closed", Title: "Why can't I pay right now?",
				Summary: "The plant may be closed. Check its opening hours.",
				Body: []HelpBlock{
					p("If the plant is closed, its page shows that it's closed and the pay button is paused. Today's price is still shown so you can plan. Try again during opening hours, which are shown on the page."),
				}},
		},
	},
	{
		Slug: "order-problems", Title: "Order problems", Icon: "warning-circle", Audience: "customers",
		Description: "For customers: payment issues, refunds, and contacting a plant.",
		Articles: []HelpArticle{
			{Slug: "paid-no-code", Title: "I paid but didn't get a code",
				Summary: "Check Receipts. Bank transfers can take a few minutes to confirm.",
				Body: []HelpBlock{
					p("Bank transfers and USSD payments can take a few minutes to confirm. Your code appears once Paystack confirms the payment."),
					p("Check Receipts on the plant's page with the phone number you paid with. If the payment still doesn't show, contact the plant with your payment details."),
				}},
			{Slug: "refunds", Title: "How do I get a refund?",
				Summary: "Refunds are handled by the plant you bought from.",
				Body: []HelpBlock{
					p("Each plant sets its own refund policy. Contact the plant directly: their phone number is at the top of their GatsToGo page. Have your order code or the phone number you paid with ready."),
				}},
			{Slug: "wrong-amount", Title: "I think I got less gas than I paid for",
				Summary: "Your order shows exactly how many kg you paid for.",
				Body: []HelpBlock{
					p("Every order records the kg you paid for, and the attendant records the kg filled. Check your order in Receipts, then speak to the plant with your code so they can check their records."),
				}},
			{Slug: "contact-plant", Title: "Contacting a plant",
				Summary: "Their phone number is at the top of their page.",
				Body: []HelpBlock{
					p("Each plant's phone number is shown at the top of its GatsToGo page, next to its address. For orders, refunds, delivery, or opening hours, contact the plant directly."),
					p("For problems with the GatsToGo website itself, contact us from the Contact page."),
				}},
		},
	},
}

// HelpArticleRef points to one article inside its category.
type HelpArticleRef struct {
	Category HelpCategory
	Article  HelpArticle
}

func FindHelpCategory(slug string) (HelpCategory, bool) {
	for _, c := range HelpCategories {
		if c.Slug == slug {
			return c, true
		}
	}
	return HelpCategory{}, false
}

func FindHelpArticle(categorySlug, articleSlug string) (HelpCategory, HelpArticle, bool) {
	c, ok := FindHelpCategory(categorySlug)
	if !ok {
		return HelpCategory{}, HelpArticle{}, false
	}
	for _, a := range c.Articles {
		if a.Slug == articleSlug {
			return c, a, true
		}
	}
	return c, HelpArticle{}, false
}

func PopularHelpArticles() []HelpArticleRef {
	var out []HelpArticleRef
	for _, c := range HelpCategories {
		for _, a := range c.Articles {
			if a.Popular {
				out = append(out, HelpArticleRef{c, a})
			}
		}
	}
	return out
}

func helpArticleCount() int {
	n := 0
	for _, c := range HelpCategories {
		n += len(c.Articles)
	}
	return n
}

func helpArticleURL(c HelpCategory, a HelpArticle) string { return "/help/" + c.Slug + "/" + a.Slug }
func helpCategoryURL(c HelpCategory) string               { return "/help/" + c.Slug }

// helpSearchEntry is one row of the index the hub's search box filters
// client-side (embedded with templ.JSONScript).
type helpSearchEntry struct {
	T string `json:"t"`
	S string `json:"s"`
	C string `json:"c"`
	U string `json:"u"`
}

func helpSearchEntries() []helpSearchEntry {
	var es []helpSearchEntry
	for _, c := range HelpCategories {
		for _, a := range c.Articles {
			es = append(es, helpSearchEntry{a.Title, a.Summary, c.Title, helpArticleURL(c, a)})
		}
	}
	return es
}

// relatedHelpArticles returns up to n other articles from the same category.
func relatedHelpArticles(c HelpCategory, current HelpArticle, n int) []HelpArticle {
	var out []HelpArticle
	for _, a := range c.Articles {
		if a.Slug != current.Slug && len(out) < n {
			out = append(out, a)
		}
	}
	return out
}
