package rules_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
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
