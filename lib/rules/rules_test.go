package rules_test

import (
	"testing"
	"time"

	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/rules"
)

func tx(description string) model.Transaction {
	return model.Transaction{
		Description: description, Account: "Liabilities:Card:Visa",
		Amount: model.Amount{Units: -8420, Scale: 2, Commodity: "CAD"},
	}
}

func engine(t *testing.T, rs ...rules.Rule) *rules.Engine {
	t.Helper()
	e, err := rules.New(rs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func only(t *testing.T, e model.Entry) model.Posting {
	t.Helper()
	if len(e.Postings) != 1 {
		t.Fatalf("want one posting, got %d: %+v", len(e.Postings), e.Postings)
	}
	return e.Postings[0]
}

func TestARuleNamesThePayeeAndTheAccountToPostTo(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme hardware`, Payee: "Acme Hardware", Category: "Expenses:Repairs:Materials"})

	got := e.Apply(tx("ACME HARDWARE #4471"))

	if got.Payee != "Acme Hardware" {
		t.Errorf("payee = %q", got.Payee)
	}
	if only(t, got).Account != "Expenses:Repairs:Materials" {
		t.Errorf("account = %q", only(t, got).Account)
	}
	if got.Uncategorized() {
		t.Error("a fully named account is categorized")
	}
}

// A rule can route the source (card/liability) leg to a sub-account, so a physical card imported on
// one registered account self-routes each matching charge to its purpose child. Routing changes only
// where the elided leg lands, never how much, so the categorized side still balances the whole line.
func TestARuleRoutesTheSourceLegToASubAccount(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, Category: "Expenses:Materials:9 Schoodic", Source: "Liabilities:PC Mastercard:9 Schoodic"})

	line := tx("KENT BUILDING SUPPLIES")
	got := e.Apply(line)

	if got.Source != "Liabilities:PC Mastercard:9 Schoodic" {
		t.Errorf("Source = %q, want the routed sub-account", got.Source)
	}
	if got.SourceAccount(line) != "Liabilities:PC Mastercard:9 Schoodic" {
		t.Errorf("SourceAccount = %q, want the routed leg to land on the child", got.SourceAccount(line))
	}
	if !got.Balances(line) {
		t.Error("routing moves the leg, not the amount: the entry must still balance the line")
	}
}

// Source is first-wins per field like every other: a specific rule routes the leg while a later,
// broader rule still names the payee it left empty.
func TestSourceIsFirstWinsPerField(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `kent`, Category: "Expenses:Materials:9 Schoodic", Source: "Liabilities:PC Mastercard:9 Schoodic"},
		rules.Rule{Match: `building`, Source: "Liabilities:PC Mastercard:Wrong", Payee: "Kent Building Supplies"},
	)

	got := e.Apply(tx("KENT BUILDING SUPPLIES"))

	if got.Source != "Liabilities:PC Mastercard:9 Schoodic" {
		t.Errorf("Source = %q, want the earlier rule's route", got.Source)
	}
	if got.Payee != "Kent Building Supplies" {
		t.Errorf("payee = %q, want the later rule to fill the gap", got.Payee)
	}
}

// The statement's sign is from the source account's point of view, so the categorized posting
// takes the opposite one. Money out of the account is money into an expense.
func TestThePostingTakesTheOppositeSignOfTheStatementLine(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	if got := only(t, e.Apply(tx("ACME HARDWARE"))); got.Amount.String() != "84.20 CAD" {
		t.Errorf("amount = %s, want 84.20 CAD", got.Amount)
	}
}

func TestMatchingIsCaseInsensitive(t *testing.T) {
	e := engine(t, rules.Rule{Match: `city water`, Category: "Expenses:Utilities:Water"})

	if got := only(t, e.Apply(tx("CITY WATER UTILITY"))); got.Account != "Expenses:Utilities:Water" {
		t.Errorf("account = %q, want the water account", got.Account)
	}
}

// Rules are ordered and the first rule to supply a field wins that field. A later, broader rule
// cannot overwrite a category an earlier, more specific rule already set.
func TestFirstMatchWinsPerField(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `coffee house`, Category: "Expenses:Meals"},
		rules.Rule{Match: `coffee`, Category: "Expenses:Wrong", Payee: "Generic Coffee"},
	)

	got := e.Apply(tx("COFFEE HOUSE 12"))

	if only(t, got).Account != "Expenses:Meals" {
		t.Errorf("account = %q, want the earlier rule's", only(t, got).Account)
	}
	// The later rule still contributes the field the earlier one left empty.
	if got.Payee != "Generic Coffee" {
		t.Errorf("payee = %q, want the later rule to fill the gap", got.Payee)
	}
}

// The books must stay complete, so a line nothing matches still posts. It posts to a top-level
// Uncategorized account rather than being guessed into Expenses or Income, which would silently
// corrupt both totals. Never guess across kinds.
func TestALineNoRuleMatchesPostsToUncategorized(t *testing.T) {
	e := engine(t)

	got := e.Apply(tx("SOME UNKNOWN MERCHANT"))

	if only(t, got).Account != model.Uncategorized {
		t.Errorf("account = %q, want %q", only(t, got).Account, model.Uncategorized)
	}
	if !got.Uncategorized() {
		t.Error("the entry should report itself uncategorized")
	}
}

// A hardware store charge could serve any property, and that fact lives on the receipt rather than
// in the description. The rule says so, and says nothing more. Expenses:Real Estate:Materials is
// still exactly right, so every total above the leaf stays honest.
func TestARuleMayStopAtAnUncategorizedLeaf(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `acme hardware`, Payee: "Acme Hardware",
		Category: "Expenses:Real Estate:Materials:Uncategorized",
	})

	got := e.Apply(tx("ACME HARDWARE #4471"))

	if only(t, got).Account != "Expenses:Real Estate:Materials:Uncategorized" {
		t.Errorf("account = %q", only(t, got).Account)
	}
	if !got.Uncategorized() {
		t.Error("an uncategorized leaf should report itself, at any depth")
	}
}

// The bank's memo is a worse payee than a rule's, and a better one than nothing.
func TestPayeeFallsBackToTheDescription(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	if got := e.Apply(tx("ACME HARDWARE #4471")); got.Payee != "ACME HARDWARE #4471" {
		t.Errorf("payee = %q, want the description", got.Payee)
	}
}

// Postings are the categorized side only, so an entry is balanced by construction: the elided
// posting against the transaction's own account absorbs whatever is left.
func TestASingleCategoryPostingBalancesTheTransaction(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	line := tx("ACME HARDWARE")
	if got := e.Apply(line); !got.Balances(line) {
		t.Errorf("entry does not balance: %+v", got.Postings)
	}
}

func TestInvalidRegexIsRejected(t *testing.T) {
	if _, err := rules.New([]rules.Rule{{Match: `([`}}); err == nil {
		t.Fatal("expected an error for an invalid regex")
	}
}

// A charge on a card already includes its sales tax, so a vendor known to be taxed splits into two
// postings: the pre-tax amount to the category, and the tax extracted from the same total to the
// tax account. The rate is tax-inclusive, exactly as apply-hst.rb did: net = total / (1 + rate).
func TestATaxedRuleSplitsTheTaxOutOfTheTotal(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `acme`, Category: "Expenses:Repairs:Materials",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC",
	})

	line := taxable("ACME HARDWARE", -11500) // 115.00 out, tax included
	got := e.Apply(line)

	if len(got.Postings) != 2 {
		t.Fatalf("want two postings, got %d: %+v", len(got.Postings), got.Postings)
	}
	net, tax := got.Postings[0], got.Postings[1]
	if net.Account != "Expenses:Repairs:Materials" || net.Amount.String() != "100.00 CAD" {
		t.Errorf("net posting = %s %s, want Expenses:Repairs:Materials 100.00 CAD", net.Account, net.Amount)
	}
	if tax.Account != "Assets:HST ITC" || tax.Amount.String() != "15.00 CAD" {
		t.Errorf("tax posting = %s %s, want Assets:HST ITC 15.00 CAD", tax.Account, tax.Amount)
	}
}

// However the rate rounds, the two postings must still account for the whole line: the entry stays
// balanced by construction, because the tax posting takes exactly what the net posting left.
func TestATaxSplitStillBalancesTheTransaction(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `acme`, Category: "Expenses:Repairs", TaxRate: "13%", TaxAccount: "Assets:HST ITC",
	})

	line := taxable("ACME HARDWARE", -8420) // 84.20 out; 13% does not divide evenly
	if got := e.Apply(line); !got.Balances(line) {
		t.Errorf("taxed entry does not balance: %+v", got.Postings)
	}
}

// A tax rate is meaningless without somewhere to post the tax, so the pair is required together.
func TestATaxRateWithoutAnAccountIsRejected(t *testing.T) {
	if _, err := rules.New([]rules.Rule{{Match: `acme`, Category: "Expenses:Repairs", TaxRate: "15%"}}); err == nil {
		t.Fatal("expected an error for a tax rate with no account")
	}
}

func taxable(description string, units int64) model.Transaction {
	return model.Transaction{
		Description: description, Account: "Liabilities:Card:Visa",
		Amount: model.Amount{Units: units, Scale: 2, Commodity: "CAD"},
	}
}

func datedTaxable(description string, units int64, date string) model.Transaction {
	tx := taxable(description, units)
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	tx.Date = parsed
	return tx
}

// The right tax treatment can depend on the category, not just the vendor: the same hardware store
// sells to a property whose tax is claimable and to one whose is not. TaxCategory scopes the split
// to the categories it names; a category outside it stays gross.
func TestTaxCategoryScopesTheSplitToMatchingCategories(t *testing.T) {
	claimable := engine(t, rules.Rule{
		Match: `kent`, Category: "Expenses:Real Estate:Materials:9 Schoodic",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxCategory: `9 schoodic`,
	})
	if got := claimable.Apply(taxable("KENT BUILDING SUPPLIES", -11500)); len(got.Postings) != 2 {
		t.Errorf("a matching category should split, got %+v", got.Postings)
	}

	gross := engine(t, rules.Rule{
		Match: `kent`, Category: "Expenses:Real Estate:Materials:22 Lisgar",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxCategory: `9 schoodic`,
	})
	got := gross.Apply(taxable("KENT BUILDING SUPPLIES", -11500))
	if len(got.Postings) != 1 {
		t.Fatalf("a category outside the scope must stay gross, got %+v", got.Postings)
	}
	if got.Postings[0].Amount.String() != "115.00 CAD" {
		t.Errorf("gross amount = %s, want the whole 115.00 CAD", got.Postings[0].Amount)
	}
}

// TaxCategory gates on the category the line finally takes, wherever it came from: a later rule can
// name the category and the earlier rule's tax still scopes against it.
func TestTaxCategoryGatesOnTheResolvedCategory(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxCategory: `9 schoodic`},
		rules.Rule{Match: `building`, Category: "Expenses:Real Estate:Materials:9 Schoodic"},
	)

	got := e.Apply(taxable("KENT BUILDING SUPPLIES", -11500))

	if len(got.Postings) != 2 {
		t.Fatalf("the later rule's category is in scope, so the tax should split: %+v", got.Postings)
	}
	if got.Postings[1].Account != "Assets:HST ITC" {
		t.Errorf("tax account = %q", got.Postings[1].Account)
	}
}

// One rule can serve every property when the tax account is derived from the category: TaxCategory's
// capture groups expand into TaxAccount, so the KENT charge categorized to a property extracts to
// that property's own tax account.
func TestTaxAccountExpandsTaxCategoryCaptures(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `kent`, Category: "Expenses:Real Estate:Materials:9 Schoodic",
		TaxRate: "15%", TaxCategory: `Materials:([^:]+)`, TaxAccount: "Expenses:Real Estate:HST:ITC:$1",
	})

	got := e.Apply(taxable("KENT BUILDING SUPPLIES", -11500))

	if len(got.Postings) != 2 {
		t.Fatalf("want a split, got %+v", got.Postings)
	}
	if got.Postings[1].Account != "Expenses:Real Estate:HST:ITC:9 Schoodic" {
		t.Errorf("tax account = %q, want the property derived from the category", got.Postings[1].Account)
	}
}

// A filed year's lines already carry their splits, so re-splitting them would double-count.
// TaxFrom bounds the rule's tax: a line dated before it stays gross, one on or after it splits.
func TestTaxFromBoundsTheSplitByDate(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `kent`, Category: "Expenses:Materials",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01",
	})

	if got := e.Apply(datedTaxable("KENT", -11500, "2025-12-31")); len(got.Postings) != 1 {
		t.Errorf("a line before TaxFrom must stay gross, got %+v", got.Postings)
	}
	if got := e.Apply(datedTaxable("KENT", -11500, "2026-01-01")); len(got.Postings) != 2 {
		t.Errorf("a line on TaxFrom should split, got %+v", got.Postings)
	}
}

// A vendor's tax is a fact about the vendor; the category is a fact about the line. OverlayTax lays
// the tax onto an entry something else categorized, so the vendor's fact survives a human picking
// the category: the asserted leg splits into its pre-tax amount and the tax, and everything else on
// the entry stays the human's.
func TestOverlayLaysTheTaxOntoACategorizedEntry(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01",
	})
	line := datedTaxable("KENT BUILDING SUPPLIES", -11500, "2026-03-05")
	entry := model.Entry{Payee: "Kent", Source: "Liabilities:Card:Reno", Postings: []model.Posting{
		{Account: "Expenses:Materials:Unit 1", Amount: line.Amount.Negate(), Comment: "the receipt said Unit 1"},
	}}

	got := e.OverlayTax(line, entry)

	if len(got.Postings) != 2 {
		t.Fatalf("want the asserted leg split in two, got %+v", got.Postings)
	}
	net, tax := got.Postings[0], got.Postings[1]
	if net.Account != "Expenses:Materials:Unit 1" || net.Amount.String() != "100.00 CAD" {
		t.Errorf("net = %s %s, want the asserted category at 100.00 CAD", net.Account, net.Amount)
	}
	if net.Comment != "the receipt said Unit 1" {
		t.Errorf("net comment = %q, want the human's note kept on the leg it was left on", net.Comment)
	}
	if tax.Account != "Assets:HST ITC" || tax.Amount.String() != "15.00 CAD" {
		t.Errorf("tax = %s %s, want Assets:HST ITC at 15.00 CAD", tax.Account, tax.Amount)
	}
	if got.Payee != "Kent" || got.Source != "Liabilities:Card:Reno" {
		t.Errorf("payee/source = %q/%q, want the assertion's kept", got.Payee, got.Source)
	}
	if !got.Balances(line) {
		t.Error("the overlaid entry must still account for the whole line")
	}
}

// Rewriting asserted history must be opted into and bounded, so a taxed rule without TaxFrom never
// overlays: it behaves exactly as it always has, splitting only the lines it categorizes itself.
func TestOverlayNeedsTaxFrom(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC"})
	line := datedTaxable("KENT", -11500, "2026-03-05")
	entry := model.Entry{Postings: []model.Posting{{Account: "Expenses:Materials", Amount: line.Amount.Negate()}}}

	if got := e.OverlayTax(line, entry); len(got.Postings) != 1 {
		t.Errorf("a rule without TaxFrom must not overlay, got %+v", got.Postings)
	}
}

// The bound is the guard against double-counting: a filed year's line, already split in the old
// ledger it was carried from, is dated before TaxFrom and stays exactly as asserted.
func TestOverlaySkipsLinesBeforeTaxFrom(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01"})
	line := datedTaxable("KENT", -11500, "2025-06-30")
	entry := model.Entry{Postings: []model.Posting{{Account: "Expenses:Materials", Amount: line.Amount.Negate()}}}

	if got := e.OverlayTax(line, entry); len(got.Postings) != 1 {
		t.Errorf("a line before TaxFrom must stay as asserted, got %+v", got.Postings)
	}
}

// Spelled-out legs are the caller's own arithmetic — a hand-made split, or a split that already
// carries its tax — so an entry with more than one posting is never touched.
func TestOverlayLeavesASpelledSplitAlone(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01"})
	line := datedTaxable("KENT", -11500, "2026-03-05")
	entry := model.Entry{Postings: []model.Posting{
		{Account: "Expenses:Materials:Unit 1", Amount: model.Amount{Units: 5000, Scale: 2, Commodity: "CAD"}},
		{Account: "Expenses:Materials:Unit 2", Amount: model.Amount{Units: 6500, Scale: 2, Commodity: "CAD"}},
	}}

	if got := e.OverlayTax(line, entry); len(got.Postings) != 2 || got.Postings[0].Amount.String() != "50.00 CAD" {
		t.Errorf("a hand-made split must stay the caller's, got %+v", got.Postings)
	}
}

// A leg already on the rule's tax account is the tax, so splitting it again would double-count.
func TestOverlaySkipsALegAlreadyOnTheTaxAccount(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01"})
	line := datedTaxable("KENT REFUND", 1500, "2026-03-05")
	entry := model.Entry{Postings: []model.Posting{{Account: "Assets:HST ITC", Amount: line.Amount.Negate()}}}

	if got := e.OverlayTax(line, entry); len(got.Postings) != 1 {
		t.Errorf("a leg already on the tax account must not split, got %+v", got.Postings)
	}
}

// The overlay reads the asserted category through the same scope Apply does: the property the human
// picked decides whether the tax splits at all and which account it lands in, so one vendor serves a
// claimable property and a gross one from one rule.
func TestOverlayScopesByTheAssertedCategory(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `kent`, TaxRate: "15%", TaxFrom: "2026-01-01",
		TaxCategory: `Materials:(9 Schoodic)`, TaxAccount: "Expenses:Real Estate:HST:ITC:$1",
	})
	line := datedTaxable("KENT BUILDING SUPPLIES", -11500, "2026-03-05")

	claimed := e.OverlayTax(line, model.Entry{Postings: []model.Posting{
		{Account: "Expenses:Real Estate:Materials:9 Schoodic", Amount: line.Amount.Negate()},
	}})
	if len(claimed.Postings) != 2 || claimed.Postings[1].Account != "Expenses:Real Estate:HST:ITC:9 Schoodic" {
		t.Errorf("a category in scope should split to its derived account, got %+v", claimed.Postings)
	}

	gross := e.OverlayTax(line, model.Entry{Postings: []model.Posting{
		{Account: "Expenses:Real Estate:Materials:22 Lisgar", Amount: line.Amount.Negate()},
	}})
	if len(gross.Postings) != 1 {
		t.Errorf("a category outside the scope must stay gross, got %+v", gross.Postings)
	}
}

// A priced or foreign leg cannot be split against the line's commodity, so it is left alone rather
// than guessed at.
func TestOverlaySkipsAForeignOrPricedLeg(t *testing.T) {
	e := engine(t, rules.Rule{Match: `kent`, TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01"})
	line := datedTaxable("KENT", -11500, "2026-03-05")
	cost := model.Amount{Units: 11500, Scale: 2, Commodity: "CAD"}
	entry := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost},
	}}

	if got := e.OverlayTax(line, entry); len(got.Postings) != 1 || got.Postings[0].Cost == nil {
		t.Errorf("a priced leg must stay whole, got %+v", got.Postings)
	}
}

// The scope and the bound qualify a tax; without one they have nothing to qualify, so they are
// refused at authoring like a rate without an account.
func TestTaxCategoryWithoutATaxIsRejected(t *testing.T) {
	if _, err := rules.New([]rules.Rule{{Match: `kent`, Category: "Expenses:Materials", TaxCategory: `9 schoodic`}}); err == nil {
		t.Fatal("expected an error for a tax category with no tax")
	}
	if _, err := rules.New([]rules.Rule{{Match: `kent`, Category: "Expenses:Materials", TaxFrom: "2026-01-01"}}); err == nil {
		t.Fatal("expected an error for a tax from-date with no tax")
	}
}

func TestAnInvalidTaxCategoryPatternIsRejected(t *testing.T) {
	_, err := rules.New([]rules.Rule{{
		Match: `kent`, Category: "Expenses:Materials",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxCategory: `([`,
	}})
	if err == nil {
		t.Fatal("expected an error for an invalid tax category pattern")
	}
}

func TestAnUnreadableTaxFromDateIsRejected(t *testing.T) {
	_, err := rules.New([]rules.Rule{{
		Match: `kent`, Category: "Expenses:Materials",
		TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "January 2026",
	}})
	if err == nil {
		t.Fatal("expected an error for a date the rule cannot read")
	}
}

// A rule carries an opaque metadata bag the engine neither reads nor validates, and Apply lands it
// on the entry. It is the seam a connector (e.g. the rent app) reads its own keys from, so a
// deposit categorized to a property can also name the lease it should be exported against.
func TestARuleCarriesItsMetadataOntoTheEntry(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `hyungjin`, Category: "Income:Real Estate:Rent:22 Lisgar Street",
		Metadata: map[string]string{"rentapp.lease": "31"},
	})

	got := e.Apply(tx("E-TRANSFER FROM HYUNGJIN SON"))

	if got.Metadata["rentapp.lease"] != "31" {
		t.Errorf("metadata = %v, want rentapp.lease=31 carried onto the entry", got.Metadata)
	}
}

// Metadata is first-wins per key, like the other fields: the earliest matching rule that supplies a
// key owns it, and a later matching rule fills only the keys still empty. So the lease follows the
// specific tenant rule even when a broader rule also matches.
func TestMetadataIsFirstWinsPerKey(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `hyungjin`, Metadata: map[string]string{"rentapp.lease": "31"}},
		rules.Rule{Match: `e-transfer`, Metadata: map[string]string{"rentapp.lease": "99", "channel": "etransfer"}},
	)

	got := e.Apply(tx("E-TRANSFER FROM HYUNGJIN SON"))

	if got.Metadata["rentapp.lease"] != "31" {
		t.Errorf("rentapp.lease = %q, want the specific rule's 31", got.Metadata["rentapp.lease"])
	}
	if got.Metadata["channel"] != "etransfer" {
		t.Errorf("channel = %q, want the later rule to fill the empty key", got.Metadata["channel"])
	}
}

// A line no rule matches, or a rule with no metadata, leaves the bag empty rather than non-nil
// noise, so a reader can treat "no metadata" and "empty metadata" the same.
func TestAnEntryWithoutRuleMetadataHasNone(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	if got := e.Apply(tx("ACME HARDWARE")); len(got.Metadata) != 0 {
		t.Errorf("metadata = %v, want none", got.Metadata)
	}
}
