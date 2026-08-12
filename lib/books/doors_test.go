package books_test

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

// Every line remembers the door it entered through: the actor that imported it. The register shows
// that door beside each line, and the twin detector groups across doors, so the map must name the
// importing actor for every line and only ever the first one -- a line is imported once.
func TestDoorsMapsEachLineToItsImportingActor(t *testing.T) {
	log := newLog()

	if _, err := books.Import(log, "connector:acmecard", []model.Transaction{
		line("aaa", 1, -8420, "ACME HARDWARE"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := books.Import(log, "statement:hand.txt", []model.Transaction{
		line("bbb", 2, -6240, "SHELL GAS #123"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	doors, err := books.Doors(log)
	if err != nil {
		t.Fatalf("Doors: %v", err)
	}
	if doors["aaa"] != "connector:acmecard" {
		t.Errorf("doors[aaa] = %q, want the connector that imported it", doors["aaa"])
	}
	if doors["bbb"] != "statement:hand.txt" {
		t.Errorf("doors[bbb] = %q, want the statement file that imported it", doors["bbb"])
	}
}

// An accrual books a synthetic line the register must also name a door for: the kind of accrual it
// is. The fold's ids are matched through the fold itself, so this test knows nothing of their shape.
func TestDoorsNameAccrualLines(t *testing.T) {
	log := newLog()
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(3), Party: "J Smith", Amount: cad(160000),
		Category: "Income:Rent", Account: "Assets:Receivable",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(4), Party: "Hydro", Amount: cad(9000),
		Category: "Expenses:Hydro", Account: "Liabilities:Payable",
	}); err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}

	txs, _, err := books.LedgerBasis(log, books.AccrualBasis)
	if err != nil {
		t.Fatalf("LedgerBasis: %v", err)
	}
	doors, err := books.Doors(log)
	if err != nil {
		t.Fatalf("Doors: %v", err)
	}
	want := map[string]string{"Assets:Receivable": "invoice", "Liabilities:Payable": "bill"}
	for _, tx := range txs {
		if doors[tx.ID] != want[tx.Account] {
			t.Errorf("doors[%s] = %q, want %q for the line parked in %s", tx.ID, doors[tx.ID], want[tx.Account], tx.Account)
		}
	}
}

// A settled accrual and the bank line that paid it are one recorded flow, not a coincidence of date
// and amount, so the settlement must be readable against the fold's own line ids.
func TestSettlementsKeyTheFoldLine(t *testing.T) {
	log := newLog()
	if _, err := books.Import(log, "connector:acme", []model.Transaction{
		line("dep", 5, 160000, "E-TRANSFER FROM J SMITH"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(3), Party: "J Smith", Amount: cad(160000),
		Category: "Income:Rent", Account: "Assets:Receivable",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := books.SettleInvoice(log, "human", inv.ID, "dep"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}

	txs, _, err := books.LedgerBasis(log, books.AccrualBasis)
	if err != nil {
		t.Fatalf("LedgerBasis: %v", err)
	}
	settled, err := books.Settlements(log)
	if err != nil {
		t.Fatalf("Settlements: %v", err)
	}
	var found bool
	for _, tx := range txs {
		if tx.Account == "Assets:Receivable" {
			found = true
			if settled[tx.ID] != "dep" {
				t.Errorf("settlements[%s] = %q, want the paying line", tx.ID, settled[tx.ID])
			}
		}
	}
	if !found {
		t.Fatal("the accrual line should be in the accrual-basis fold")
	}
}
