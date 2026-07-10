package rules_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
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
