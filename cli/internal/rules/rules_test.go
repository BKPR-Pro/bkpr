package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
)

func tx(description string) model.Transaction {
	return model.Transaction{Description: description, Account: "Liabilities:Card:Visa"}
}

func engine(t *testing.T, rs ...rules.Rule) *rules.Engine {
	t.Helper()
	e, err := rules.New(rs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// The common shape: a specific rule names the payee and category, and a trailing catch-all
// supplies the account's default balancing posting.
func TestSpecificRuleThenCatchAllBalance(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `acme hardware`, Payee: "Acme Hardware", Category: "Expenses:Repairs:Materials"},
		rules.Rule{Match: `.`, Balance: "Liabilities:Card:Visa"},
	)

	got := e.Apply(tx("ACME HARDWARE #4471"))

	if got.NeedsReview {
		t.Fatalf("did not expect review, got reason %q", got.Reason)
	}
	if got.Payee != "Acme Hardware" || got.Category != "Expenses:Repairs:Materials" {
		t.Errorf("payee/category = %q/%q", got.Payee, got.Category)
	}
	if got.Balance != "Liabilities:Card:Visa" {
		t.Errorf("balance = %q", got.Balance)
	}
	if !got.Categorized() {
		t.Error("expected a categorized decision")
	}
}

func TestMatchingIsCaseInsensitive(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `city water`, Category: "Expenses:Utilities:Water"},
		rules.Rule{Match: `.`, Balance: "Assets:Bank:Chequing"},
	)

	if got := e.Apply(tx("CITY WATER UTILITY")); got.Category != "Expenses:Utilities:Water" {
		t.Errorf("category = %q, want the water account", got.Category)
	}
}

// Rules are ordered and the first rule to supply a field wins that field. A later, broader rule
// cannot overwrite a category an earlier, more specific rule already set.
func TestFirstMatchWinsPerField(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `coffee house`, Category: "Expenses:Meals"},
		rules.Rule{Match: `coffee`, Category: "Expenses:Wrong", Payee: "Generic Coffee"},
		rules.Rule{Match: `.`, Balance: "Assets:Bank:Chequing"},
	)

	got := e.Apply(tx("COFFEE HOUSE 12"))

	if got.Category != "Expenses:Meals" {
		t.Errorf("category = %q, want the earlier rule's", got.Category)
	}
	// The later rule still contributes the field the earlier one left empty.
	if got.Payee != "Generic Coffee" {
		t.Errorf("payee = %q, want the later rule to fill the gap", got.Payee)
	}
}

// A line nothing categorizes is never guessed at. It goes to the human.
func TestUncategorizedNeedsReview(t *testing.T) {
	e := engine(t, rules.Rule{Match: `.`, Balance: "Assets:Bank:Chequing"})

	got := e.Apply(tx("SOME UNKNOWN MERCHANT"))

	if !got.NeedsReview {
		t.Fatal("expected review")
	}
	if got.Reason == "" {
		t.Error("expected a reason explaining why")
	}
	if got.Categorized() {
		t.Error("a review decision is not categorized")
	}
	// It still learned the balance account, which the human should not have to supply.
	if got.Balance != "Assets:Bank:Chequing" {
		t.Errorf("balance = %q", got.Balance)
	}
}

// An ambiguous merchant still posts. The category is a defensible default, so the line is flagged
// rather than withheld: guessing a leaf costs insight, and gating costs the thing this tool exists
// to avoid.
func TestUncertainRuleCategorizesAndFlags(t *testing.T) {
	e := engine(t,
		rules.Rule{
			Match: `acme hardware`, Payee: "Acme Hardware",
			Category:  "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2",
			Uncertain: true, Reason: "hardware could serve any property",
		},
		rules.Rule{Match: `.`, Balance: "Assets:Bank:Chequing"},
	)

	got := e.Apply(tx("ACME HARDWARE #4471"))

	if got.Category != "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2" {
		t.Errorf("category = %q, want the default to still be applied", got.Category)
	}
	if !got.Categorized() {
		t.Error("an uncertain line is still categorized, just not confidently")
	}
	if !got.NeedsReview {
		t.Error("an uncertain line should be flagged")
	}
	if got.Reason != "hardware could serve any property" {
		t.Errorf("reason = %q", got.Reason)
	}
}

// The catch-all matches every line. It must not clear the uncertainty of the rule that actually
// supplied the category.
func TestCatchAllDoesNotClobberUncertainty(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `acme`, Category: "Expenses:Materials", Uncertain: true},
		rules.Rule{Match: `.`, Balance: "Assets:Bank:Chequing"},
	)

	if got := e.Apply(tx("ACME HARDWARE")); !got.NeedsReview {
		t.Error("the catch-all cleared the uncertain flag")
	}
}

// A confident rule stays confident even when a later uncertain rule also matches.
func TestCertainRuleIsNotFlagged(t *testing.T) {
	e := engine(t,
		rules.Rule{Match: `city water`, Category: "Expenses:Utilities:Water"},
		rules.Rule{Match: `water`, Category: "Expenses:Wrong", Uncertain: true, Reason: "nope"},
	)

	if got := e.Apply(tx("CITY WATER UTILITY")); got.NeedsReview {
		t.Errorf("unexpected flag: %s", got.Reason)
	}
}

// A statement line already knows its own account, so a rule set needs no catch-all just to name
// the balancing posting. Rules only override it for transfers.
func TestBalanceDefaultsToTheTransactionsOwnAccount(t *testing.T) {
	e := engine(t, rules.Rule{Match: `acme`, Category: "Expenses:Repairs"})

	got := e.Apply(tx("ACME HARDWARE"))

	if got.Balance != "Liabilities:Card:Visa" {
		t.Errorf("balance = %q, want the transaction's own account", got.Balance)
	}
	if got.NeedsReview {
		t.Errorf("unexpected review: %s", got.Reason)
	}
}

func TestInvalidRegexIsRejected(t *testing.T) {
	if _, err := rules.New([]rules.Rule{{Match: `([`}}); err == nil {
		t.Fatal("expected an error for an invalid regex")
	}
}

func TestLoadFromJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	body := `[
	  {"match": "shell|petro", "payee": "Fuel Stop", "category": "Expenses:Auto:Fuel"},
	  {"match": ".", "balance": "Liabilities:Card:Visa"}
	]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	e, err := rules.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := e.Apply(tx("SHELL GAS #123"))
	if got.Category != "Expenses:Auto:Fuel" || got.Payee != "Fuel Stop" {
		t.Errorf("got %+v", got)
	}
}
