package source_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
)

func signedMapping() source.CSV {
	return source.CSV{
		Account:     "Assets:Bank:Chequing",
		Currency:    "CAD",
		Date:        "Date",
		Description: "Description",
		Amount:      "Amount",
		DateFormat:  "2006-01-02",
	}
}

func TestReadsSignedAmountCSV(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Amount\n" +
			"2026-03-01,ACME HARDWARE #4471,-84.20\n" +
			"2026-03-02,RENT E-TRANSFER FROM J SMITH,1600.00\n")

	txs, err := source.ReadCSV(in, signedMapping(), "statement.csv")
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}

	first := txs[0]
	if first.Account != "Assets:Bank:Chequing" {
		t.Errorf("account = %q", first.Account)
	}
	if got := first.Date.Format("2006-01-02"); got != "2026-03-01" {
		t.Errorf("date = %q", got)
	}
	if got := first.Amount.String(); got != "-84.20 CAD" {
		t.Errorf("amount = %q, want -84.20 CAD", got)
	}
	if first.Description != "ACME HARDWARE #4471" {
		t.Errorf("description = %q", first.Description)
	}
	if got := txs[1].Amount.String(); got != "1600.00 CAD" {
		t.Errorf("amount = %q, want 1600.00 CAD", got)
	}
	if first.Raw["Description"] == "" {
		t.Error("expected raw columns to be kept for auditing")
	}
}

// Banks format money inconsistently. Accept the common shapes rather than making the user
// pre-clean their export.
func TestParsesMessyAmounts(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Amount\n" +
			"2026-03-01,A,\"$1,234.56\"\n" +
			"2026-03-02,B,(45.00)\n" +
			"2026-03-03,C,+12\n")

	txs, err := source.ReadCSV(in, signedMapping(), "statement.csv")
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}

	want := []string{"1234.56 CAD", "-45.00 CAD", "12 CAD"}
	for i, w := range want {
		if got := txs[i].Amount.String(); got != w {
			t.Errorf("row %d amount = %q, want %q", i, got, w)
		}
	}
}

// Many banks export debits and credits as separate columns instead of one signed column.
func TestDebitCreditColumns(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Debit,Credit\n" +
			"2026-03-01,PURCHASE,84.20,\n" +
			"2026-03-02,DEPOSIT,,1600.00\n")

	m := source.CSV{
		Account: "Assets:Bank:Chequing", Currency: "CAD", Date: "Date", Description: "Description",
		Debit: "Debit", Credit: "Credit", DateFormat: "2006-01-02",
	}

	txs, err := source.ReadCSV(in, m, "statement.csv")
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if got := txs[0].Amount.String(); got != "-84.20 CAD" {
		t.Errorf("debit amount = %q, want -84.20 CAD", got)
	}
	if got := txs[1].Amount.String(); got != "1600.00 CAD" {
		t.Errorf("credit amount = %q, want 1600.00 CAD", got)
	}
}

// The ID is the idempotency root: re-importing a statement must produce the same IDs, or the
// hub would double-record. Two genuinely identical lines are a real thing (the same charge
// twice in a day) and must stay distinct.
func TestIDsAreStableAndDistinguishIdenticalLines(t *testing.T) {
	body := "Date,Description,Amount\n" +
		"2026-03-01,COFFEE HOUSE 12,-5.00\n" +
		"2026-03-01,COFFEE HOUSE 12,-5.00\n" +
		"2026-03-01,ACME HARDWARE,-5.00\n"

	first, err := source.ReadCSV(strings.NewReader(body), signedMapping(), "statement.csv")
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ReadCSV(strings.NewReader(body), signedMapping(), "statement.csv")
	if err != nil {
		t.Fatal(err)
	}

	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("row %d: re-import changed the ID (%q vs %q)", i, first[i].ID, second[i].ID)
		}
		if first[i].ID == "" {
			t.Errorf("row %d: empty ID", i)
		}
	}
	if first[0].ID == first[1].ID {
		t.Error("two identical lines collapsed to one ID; the second charge would be lost")
	}
	if first[0].ID == first[2].ID {
		t.Error("different descriptions produced the same ID")
	}
}

// The fingerprint keys on the door a line entered through (a file's name, a connector's name),
// never on the book account it lands in. Re-pointing a connector to a new account must not move
// its history's ids, or every backfill after the re-point re-lands the overlap as duplicates.
func TestFingerprintKeysOnTheDoorNotTheAccount(t *testing.T) {
	body := "Date,Description,Amount\n2026-03-01,MONTHLY FEE,-4.00\n"

	repointed := signedMapping()
	repointed.Account = "Assets:Bank:Chequing New"

	before, err := source.ReadCSV(strings.NewReader(body), signedMapping(), "rbc-chequing")
	if err != nil {
		t.Fatal(err)
	}
	after, err := source.ReadCSV(strings.NewReader(body), repointed, "rbc-chequing")
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID != after[0].ID {
		t.Errorf("re-pointing the account moved the ID (%q vs %q); a backfill would duplicate history",
			before[0].ID, after[0].ID)
	}

	otherDoor, err := source.ReadCSV(strings.NewReader(body), signedMapping(), "rbc-visa")
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID == otherDoor[0].ID {
		t.Error("two doors produced one ID; the second account's real line would be skipped as a duplicate")
	}
}

func TestMissingColumnIsAnError(t *testing.T) {
	in := strings.NewReader("Date,Memo,Amount\n2026-03-01,X,-1.00\n")
	if _, err := source.ReadCSV(in, signedMapping(), "statement.csv"); err == nil {
		t.Fatal("expected an error naming the missing Description column")
	}
}

func TestBadDateIsAnError(t *testing.T) {
	in := strings.NewReader("Date,Description,Amount\nnot-a-date,X,-1.00\n")
	if _, err := source.ReadCSV(in, signedMapping(), "statement.csv"); err == nil {
		t.Fatal("expected an error for an unparseable date")
	}
}
