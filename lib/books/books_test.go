package books_test

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

func newLog() *eventlog.Log { return eventlog.New(eventlog.NewMemory()) }

func on(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }

func cad(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }

func line(id string, day int, cents int64, description string) model.Transaction {
	return model.Transaction{
		ID: id, Account: "Assets:Bank:Chequing", Date: on(day),
		Amount: cad(cents), Description: description,
		Raw: map[string]string{"Description": description},
	}
}

func TestImportRecordsEveryLine(t *testing.T) {
	log := newLog()

	got, err := books.Import(log, "statement:march.csv", []model.Transaction{
		line("a", 1, -6240, "SHELL GAS #123"),
		line("b", 5, 160000, "E-TRANSFER FROM J SMITH"),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if got.Imported != 2 || got.Skipped != 0 {
		t.Errorf("got %+v, want 2 imported and 0 skipped", got)
	}
	txs, err := books.Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}
}

// A note on the source leg is part of what a line arrived carrying, so it must survive the log: the
// import stores it and the fold reads it back, or a ledger file's note on the account a movement came
// from would be lost the moment it was recorded.
func TestImportKeepsTheSourceLegNote(t *testing.T) {
	log := newLog()
	tx := line("a", 1, -6240, "SHELL GAS #123")
	tx.Comment = "cash on hand"

	if _, err := books.Import(log, "statement:march.csv", []model.Transaction{tx}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	txs, err := books.Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 || txs[0].Comment != "cash on hand" {
		t.Fatalf("source note lost across the log: %+v", txs)
	}
}

// Statements overlap. A line already in the log is skipped rather than booked a second time,
// which is the whole of conflict class two.
func TestReimportingAnOverlappingStatementIsANoOp(t *testing.T) {
	log := newLog()
	march := []model.Transaction{
		line("a", 1, -6240, "SHELL GAS #123"),
		line("b", 5, 160000, "E-TRANSFER FROM J SMITH"),
	}
	april := []model.Transaction{
		line("b", 5, 160000, "E-TRANSFER FROM J SMITH"), // the overlap
		line("c", 9, -14203, "POWER CO"),
	}

	if _, err := books.Import(log, "statement:march.csv", march); err != nil {
		t.Fatalf("march: %v", err)
	}
	got, err := books.Import(log, "statement:april.csv", april)
	if err != nil {
		t.Fatalf("april: %v", err)
	}

	if got.Imported != 1 || got.Skipped != 1 {
		t.Errorf("got %+v, want 1 imported and 1 skipped", got)
	}
	txs, _ := books.Transactions(log)
	if len(txs) != 3 {
		t.Fatalf("got %d transactions, want 3: the rent payment was booked twice", len(txs))
	}
}

// Two genuinely identical charges on one day are two charges. Their fingerprints differ, so
// nothing about deduplication may collapse them.
func TestTwoIdenticalChargesOnOneDayStayTwoCharges(t *testing.T) {
	log := newLog()

	if _, err := books.Import(log, "statement:march.csv", []model.Transaction{
		line("fp-1", 7, -500, "COFFEE HOUSE 12"),
		line("fp-2", 7, -500, "COFFEE HOUSE 12"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	txs, _ := books.Transactions(log)
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}
}

func TestTransactionsFoldBackWithEveryFieldIntact(t *testing.T) {
	log := newLog()
	want := line("a", 2, -8420, "ACME HARDWARE #4471")

	if _, err := books.Import(log, "statement:march.csv", []model.Transaction{want}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	txs, _ := books.Transactions(log)
	got := txs[0]

	switch {
	case got.ID != want.ID:
		t.Errorf("id = %q", got.ID)
	case got.Account != want.Account:
		t.Errorf("account = %q", got.Account)
	case got.Amount != want.Amount:
		t.Errorf("amount = %v, want %v (commodity and all)", got.Amount, want.Amount)
	case !got.Date.Equal(want.Date):
		t.Errorf("date = %s", got.Date)
	case got.Description != want.Description:
		t.Errorf("description = %q", got.Description)
	case got.Raw["Description"] != want.Description:
		t.Errorf("raw = %v, want the original columns kept for auditing", got.Raw)
	}
}

// A ledger reads best in date order, and statements arrive in whatever order they arrive.
func TestTransactionsFoldBackInDateOrder(t *testing.T) {
	log := newLog()

	if _, err := books.Import(log, "statement:april.csv", []model.Transaction{line("c", 9, -1, "C")}); err != nil {
		t.Fatal(err)
	}
	if _, err := books.Import(log, "statement:march.csv", []model.Transaction{
		line("b", 5, -1, "B"), line("a", 1, -1, "A"),
	}); err != nil {
		t.Fatal(err)
	}

	txs, _ := books.Transactions(log)
	for i, want := range []string{"A", "B", "C"} {
		if txs[i].Description != want {
			t.Fatalf("position %d is %q, want %q", i, txs[i].Description, want)
		}
	}
}

// The fingerprint is the idempotency root. A line without one cannot be deduplicated, so it must
// not reach the log at all. The log has no transaction, so the whole statement is refused before
// any of it is written, rather than leaving the lines before the bad one behind.
func TestABadLineRefusesTheWholeStatement(t *testing.T) {
	log := newLog()

	_, err := books.Import(log, "statement:march.csv", []model.Transaction{
		line("a", 1, -6240, "SHELL GAS #123"),
		{Account: "Assets:Bank:Chequing", Date: on(2), Amount: cad(-100), Description: "NO FINGERPRINT"},
	})
	if err == nil {
		t.Fatal("imported a transaction with no fingerprint")
	}

	txs, _ := books.Transactions(log)
	if len(txs) != 0 {
		t.Fatalf("got %d transactions, want 0: the good line before the bad one was written", len(txs))
	}
}
