package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
)

func tx(description string) model.Transaction {
	return model.Transaction{Description: description, Account: "Liabilities:Card:Visa", AmountCents: -8420}
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

	if got.Pending {
		t.Fatalf("did not expect a flag, got reason %q", got.Reason)
	}
	if got.Payee != "Acme Hardware" {
		t.Errorf("payee = %q", got.Payee)
	}
	if only(t, got).Account != "Expenses:Repairs:Materials" {
		t.Errorf("account = %q", only(t, got).Account)
	}
}

// The statement's sign is from the source account's point of view, so the categorized posting
// takes the opposite one. Money out of the account is money into an expense.
func TestThePostingTakesTheOppositeSignOfTheStatementLine(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	if got := only(t, e.Apply(tx("ACME HARDWARE"))); got.AmountCents != 8420 {
		t.Errorf("amount = %d, want 8420", got.AmountCents)
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

// The one thing never guessed at is the kind. A line nothing categorizes still posts, because the
// books must stay complete, but it parks in a top-level suspense account rather than landing in
// Expenses or Income and silently corrupting both totals.
func TestUncategorizedLinePostsToSuspenseAndIsFlagged(t *testing.T) {
	e := engine(t)

	got := e.Apply(tx("SOME UNKNOWN MERCHANT"))

	if only(t, got).Account != model.SuspenseAccount {
		t.Errorf("account = %q, want suspense", only(t, got).Account)
	}
	if !got.Pending {
		t.Error("expected a flag")
	}
	if got.Reason == "" {
		t.Error("expected a reason explaining why")
	}
}

// An ambiguous merchant still posts. The category is a defensible default, so the line is flagged
// rather than withheld: guessing a leaf costs insight, and gating costs the thing this tool exists
// to avoid.
func TestUncertainRuleStillPostsToItsCategoryAndIsFlagged(t *testing.T) {
	e := engine(t, rules.Rule{
		Match: `acme hardware`, Payee: "Acme Hardware",
		Category:  "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2",
		Uncertain: true, Reason: "hardware could serve any property",
	})

	got := e.Apply(tx("ACME HARDWARE #4471"))

	if only(t, got).Account != "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2" {
		t.Errorf("account = %q, want the default to still be applied", only(t, got).Account)
	}
	if !got.Pending {
		t.Error("an uncertain line should be flagged")
	}
	if got.Reason != "hardware could serve any property" {
		t.Errorf("reason = %q", got.Reason)
	}
}

// A later rule that matches for some other reason must not clear the uncertainty of the rule that
// actually supplied the account.
func TestALaterRuleDoesNotClobberUncertainty(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `acme`, Category: "Expenses:Materials", Uncertain: true},
		rules.Rule{Match: `.`, Payee: "Somebody"},
	)

	if got := e.Apply(tx("ACME HARDWARE")); !got.Pending {
		t.Error("a later rule cleared the uncertain flag")
	}
}

// A confident rule stays confident even when a later uncertain rule also matches.
func TestCertainRuleIsNotFlagged(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `city water`, Category: "Expenses:Utilities:Water"},
		rules.Rule{Match: `water`, Category: "Expenses:Wrong", Uncertain: true, Reason: "nope"},
	)

	if got := e.Apply(tx("CITY WATER UTILITY")); got.Pending {
		t.Errorf("unexpected flag: %s", got.Reason)
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

func TestLoadFromJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	body := `[{"match": "shell|petro", "payee": "Fuel Stop", "category": "Expenses:Auto:Fuel"}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	e, err := rules.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := e.Apply(tx("SHELL GAS #123"))
	if got.Payee != "Fuel Stop" || only(t, got).Account != "Expenses:Auto:Fuel" {
		t.Errorf("got %+v", got)
	}
}
