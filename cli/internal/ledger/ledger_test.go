package ledger_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/cli/internal/ledger"
	"github.com/dallasread/bookkeeper/cli/internal/model"
)

func on(day int) time.Time {
	return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC)
}

func write(t *testing.T, tx model.Transaction, d model.Decision) string {
	t.Helper()
	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Decision{d}, "CAD"); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	return buf.String()
}

// Money out of the source account debits the expense and credits the account, which is elided.
// Bank data is real, so the entry is cleared.
func TestExpenseEntry(t *testing.T) {
	tx := model.Transaction{
		Date: on(2), AmountCents: -8420,
		Description: "ACME HARDWARE #4471", Account: "Assets:Bank:Chequing",
	}
	d := model.Decision{Payee: "Acme Hardware", Category: "Expenses:Repairs:Materials", Balance: "Assets:Bank:Chequing"}

	want := "2026/03/02  * Acme Hardware\n" +
		"  Expenses:Repairs:Materials  84.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Money into the source account credits income. The sign flips relative to the statement line.
func TestIncomeEntry(t *testing.T) {
	tx := model.Transaction{
		Date: on(5), AmountCents: 160000,
		Description: "E-TRANSFER FROM J SMITH", Account: "Assets:Bank:Chequing",
	}
	d := model.Decision{Payee: "J. Smith", Category: "Income:Real Estate:Rent:123 Example Street", Balance: "Assets:Bank:Chequing"}

	got := write(t, tx, d)

	if !strings.Contains(got, "Income:Real Estate:Rent:123 Example Street  -1600.00 CAD") {
		t.Errorf("income posting should be negative:\n%s", got)
	}
}

// A line the machine could not categorize is never guessed at, but it is not dropped either: the
// books stay complete. It is marked pending so it is trivial to find, and carries its reason.
func TestNeedsReviewEntryIsPendingAndExplained(t *testing.T) {
	tx := model.Transaction{
		Date: on(12), AmountCents: -3999,
		Description: "UNKNOWN MERCHANT 88", Account: "Assets:Bank:Chequing",
	}
	d := model.Decision{Balance: "Assets:Bank:Chequing", NeedsReview: true, Reason: "no rule supplied a category"}

	got := write(t, tx, d)

	if !strings.HasPrefix(got, "2026/03/12  ! UNKNOWN MERCHANT 88\n") {
		t.Errorf("want a pending flag and the description as payee:\n%s", got)
	}
	if !strings.Contains(got, "  ; needs review: no rule supplied a category\n") {
		t.Errorf("want the reason as a comment:\n%s", got)
	}
	if !strings.Contains(got, ledger.SuspenseAccount) {
		t.Errorf("want the suspense account:\n%s", got)
	}
}

// An ambiguous merchant posts to its real category, not to suspense. The books are complete and
// the leaf is a defensible guess; the pending flag is how you find it again.
func TestUncertainLinePostsToItsCategoryAndIsPending(t *testing.T) {
	tx := model.Transaction{
		Date: on(2), AmountCents: -8420,
		Description: "ACME HARDWARE #4471", Account: "Assets:Bank:Chequing",
	}
	d := model.Decision{
		Payee: "Acme Hardware", Category: "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2",
		Balance: "Assets:Bank:Chequing", NeedsReview: true, Reason: "hardware could serve any property",
	}

	got := write(t, tx, d)

	if !strings.HasPrefix(got, "2026/03/02  ! Acme Hardware\n") {
		t.Errorf("want a pending flag with the real payee:\n%s", got)
	}
	if !strings.Contains(got, "  ; needs review: hardware could serve any property\n") {
		t.Errorf("want the reason as a comment:\n%s", got)
	}
	if !strings.Contains(got, "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 2  84.20 CAD") {
		t.Errorf("an uncertain line still posts to its category:\n%s", got)
	}
	if strings.Contains(got, ledger.SuspenseAccount) {
		t.Errorf("an uncertain line must not go to suspense:\n%s", got)
	}
}

// The one thing never guessed at is the kind. An unclassified deposit must not land in Expenses,
// which would silently corrupt both totals. It goes to a top-level suspense account instead.
func TestUnknownKindGoesToSuspenseNotExpenses(t *testing.T) {
	tx := model.Transaction{
		Date: on(6), AmountCents: 250000,
		Description: "MYSTERY DEPOSIT", Account: "Assets:Bank:Chequing",
	}
	d := model.Decision{Balance: "Assets:Bank:Chequing", NeedsReview: true, Reason: "no rule supplied a category"}

	got := write(t, tx, d)

	if strings.Contains(got, "Expenses") || strings.Contains(got, "Income") {
		t.Errorf("an unclassified line must not touch Income or Expenses:\n%s", got)
	}
	if !strings.Contains(got, "Suspense  -2500.00 CAD") {
		t.Errorf("want the deposit parked in suspense:\n%s", got)
	}
}

func TestEntriesAreSeparatedByBlankLines(t *testing.T) {
	txs := []model.Transaction{
		{Date: on(1), AmountCents: -100, Description: "A", Account: "Assets:Bank:Chequing"},
		{Date: on(2), AmountCents: -200, Description: "B", Account: "Assets:Bank:Chequing"},
	}
	ds := []model.Decision{
		{Payee: "A", Category: "Expenses:X", Balance: "Assets:Bank:Chequing"},
		{Payee: "B", Category: "Expenses:Y", Balance: "Assets:Bank:Chequing"},
	}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, txs, ds, "CAD"); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "Assets:Bank:Chequing\n\n2026/03/02") {
		t.Errorf("entries should be separated by a blank line:\n%s", buf.String())
	}
}

func TestCurrencyIsConfigurable(t *testing.T) {
	tx := model.Transaction{Date: on(1), AmountCents: -100, Description: "A", Account: "Assets:Bank:Chequing"}
	d := model.Decision{Payee: "A", Category: "Expenses:X", Balance: "Assets:Bank:Chequing"}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Decision{d}, "USD"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1.00 USD") {
		t.Errorf("want USD:\n%s", buf.String())
	}
}

func TestMismatchedLengthsIsAnError(t *testing.T) {
	err := ledger.WriteAll(&bytes.Buffer{}, []model.Transaction{{}}, nil, "CAD")
	if err == nil {
		t.Fatal("expected an error when decisions do not line up with transactions")
	}
}
