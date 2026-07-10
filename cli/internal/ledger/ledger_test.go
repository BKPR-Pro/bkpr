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

func chequing(day int, cents int64, description string) model.Transaction {
	return model.Transaction{
		Date: on(day), AmountCents: cents,
		Description: description, Account: "Assets:Bank:Chequing",
	}
}

func write(t *testing.T, tx model.Transaction, e model.Entry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Entry{e}, "CAD"); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	return buf.String()
}

// Money out of the source account debits the expense and credits the account, which is elided.
func TestExpenseEntry(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee:    "Acme Hardware",
		Postings: []model.Posting{{Account: "Expenses:Repairs:Materials", AmountCents: 8420}},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  Expenses:Repairs:Materials  84.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Money into the source account credits income. The sign flips relative to the statement line.
func TestIncomeEntry(t *testing.T) {
	tx := chequing(5, 160000, "E-TRANSFER FROM J SMITH")
	e := model.Entry{
		Payee:    "J. Smith",
		Postings: []model.Posting{{Account: "Income:Real Estate:Rent:123 Example Street", AmountCents: -160000}},
	}

	if got := write(t, tx, e); !strings.Contains(got, "Income:Real Estate:Rent:123 Example Street  -1600.00 CAD") {
		t.Errorf("income posting should be negative:\n%s", got)
	}
}

// Every line bookkeeper writes came off a bank statement, so every line has cleared the bank.
// The pending flag means the bank has not reported a transaction yet, and nothing here is that.
func TestEveryEntryIsCleared(t *testing.T) {
	tx := chequing(12, -3999, "UNKNOWN MERCHANT 88")
	e := model.Entry{
		Payee:    "UNKNOWN MERCHANT 88",
		Postings: []model.Posting{{Account: model.Uncategorized, AmountCents: 3999}},
	}

	got := write(t, tx, e)

	if !strings.HasPrefix(got, "2026/03/12  * UNKNOWN MERCHANT 88\n") {
		t.Errorf("an imported line is cleared, whatever its account:\n%s", got)
	}
	if strings.Contains(got, "!") {
		t.Errorf("nothing imported from a statement is pending:\n%s", got)
	}
}

// The account path is the only marker there is. Nothing annotates it.
func TestAnUncategorizedLineCarriesNoAnnotation(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee:    "Acme Hardware",
		Postings: []model.Posting{{Account: "Expenses:Real Estate:Materials:Uncategorized", AmountCents: 8420}},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  Expenses:Real Estate:Materials:Uncategorized  84.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// One charge, two properties. This is the whole reason postings replaced a single category: the
// true answer is often not one account, and it lives on a receipt rather than in the description.
func TestASplitWritesEveryPostingAndStillElidesTheSourceAccount(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee: "Acme Hardware",
		Postings: []model.Posting{
			{Account: "Expenses:Materials:Unit 1", AmountCents: 4000},
			{Account: "Expenses:Materials:Unit 2", AmountCents: 4420},
		},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  Expenses:Materials:Unit 1  40.00 CAD\n" +
		"  Expenses:Materials:Unit 2  44.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The artifact is the books. An entry whose postings do not account for the whole statement line
// would write a silently wrong ledger, so it is refused rather than emitted.
func TestPostingsThatDoNotAccountForTheLineAreRefused(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee: "Acme Hardware",
		Postings: []model.Posting{
			{Account: "Expenses:Materials:Unit 1", AmountCents: 4000},
			{Account: "Expenses:Materials:Unit 2", AmountCents: 400},
		},
	}

	var buf bytes.Buffer
	err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Entry{e}, "CAD")
	if err == nil {
		t.Fatalf("wrote an unbalanced entry:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "84.20") {
		t.Errorf("the error should say what the line was worth: %v", err)
	}
}

func TestEntriesAreSeparatedByBlankLines(t *testing.T) {
	txs := []model.Transaction{chequing(1, -100, "A"), chequing(2, -200, "B")}
	entries := []model.Entry{
		{Payee: "A", Postings: []model.Posting{{Account: "Expenses:X", AmountCents: 100}}},
		{Payee: "B", Postings: []model.Posting{{Account: "Expenses:Y", AmountCents: 200}}},
	}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, txs, entries, "CAD"); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "Assets:Bank:Chequing\n\n2026/03/02") {
		t.Errorf("entries should be separated by a blank line:\n%s", buf.String())
	}
}

func TestCurrencyIsConfigurable(t *testing.T) {
	tx := chequing(1, -100, "A")
	e := model.Entry{Payee: "A", Postings: []model.Posting{{Account: "Expenses:X", AmountCents: 100}}}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Entry{e}, "USD"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1.00 USD") {
		t.Errorf("want USD:\n%s", buf.String())
	}
}

func TestMismatchedLengthsIsAnError(t *testing.T) {
	if err := ledger.WriteAll(&bytes.Buffer{}, []model.Transaction{{}}, nil, "CAD"); err == nil {
		t.Fatal("expected an error when entries do not line up with transactions")
	}
}
