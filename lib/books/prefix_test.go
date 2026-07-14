package books

import (
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// prefixLog holds lines whose fingerprints share fronts, the shapes prefix matching must tell
// apart: two ids diverging mid-string, and an id that is a whole prefix of its own second sighting.
func prefixLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log := eventlog.New(eventlog.NewMemory())
	cad := func(units int64) model.Amount { return model.Amount{Units: units, Scale: 2, Commodity: "CAD"} }
	if _, err := Import(log, "statement:test", []model.Transaction{
		{ID: "aaaa1111bbbb2222", Account: "Assets:Bank", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			Amount: cad(-6240), Description: "ONE"},
		{ID: "aaaa1111cccc3333", Account: "Assets:Bank", Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			Amount: cad(-8420), Description: "TWO"},
		{ID: "dddd4444eeee5555", Account: "Assets:Bank", Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			Amount: cad(-500), Description: "COFFEE"},
		{ID: "dddd4444eeee5555-1", Account: "Assets:Bank", Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			Amount: cad(-500), Description: "COFFEE"},
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	return log
}

// A fingerprint is quoted the way a git hash is: enough of the front to be unique names the line.
func TestTransactionResolvesAUniquePrefix(t *testing.T) {
	tx, err := Transaction(prefixLog(t), "aaaa1111b")
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if tx.ID != "aaaa1111bbbb2222" {
		t.Errorf("resolved %q, want the one line it uniquely begins", tx.ID)
	}
}

// An ambiguous prefix is refused by name, listing what it could have meant, never guessed.
func TestTransactionRefusesAnAmbiguousPrefix(t *testing.T) {
	_, err := Transaction(prefixLog(t), "aaaa1111")
	if err == nil {
		t.Fatal("an ambiguous prefix should be refused")
	}
	if !strings.Contains(err.Error(), "aaaa1111bbbb2222") || !strings.Contains(err.Error(), "aaaa1111cccc3333") {
		t.Errorf("error %q should name both candidates", err)
	}
}

// Below four characters only an exact id matches, so a stray short argument cannot land on an
// arbitrary line.
func TestTransactionIgnoresATooShortPrefix(t *testing.T) {
	if _, err := Transaction(prefixLog(t), "aaa"); err == nil {
		t.Error("a three-character prefix should not resolve")
	}
}

// The second sighting of an identical line extends the first's fingerprint with -1, so an exact
// match must win before prefix matching can call it ambiguous.
func TestTransactionExactMatchWinsOverItsOwnExtension(t *testing.T) {
	tx, err := Transaction(prefixLog(t), "dddd4444eeee5555")
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if tx.ID != "dddd4444eeee5555" {
		t.Errorf("resolved %q, want the exact id", tx.ID)
	}
}

// A correction made through a prefix must key to the whole fingerprint, or the fold would never
// find it again.
func TestCategorizeByPrefixKeysToTheFullFingerprint(t *testing.T) {
	log := prefixLog(t)
	tx, err := Transaction(log, "aaaa1111b")
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	post := []model.Posting{{Account: "Expenses:Travel:Fuel", Amount: tx.Amount.Negate()}}
	if err := Categorize(log, "human", "", "aaaa1111b", "", "Fuel Stop", post); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	asserted, err := assertions(log)
	if err != nil {
		t.Fatalf("assertions: %v", err)
	}
	if _, ok := asserted["aaaa1111bbbb2222"]; !ok {
		t.Errorf("assertion keys = %v, want the full fingerprint, not the prefix", asserted)
	}
}

// A void through a prefix drops the line it resolved to.
func TestVoidByPrefixDropsTheResolvedLine(t *testing.T) {
	log := prefixLog(t)
	if err := VoidTransaction(log, "human", "typo", "aaaa1111c"); err != nil {
		t.Fatalf("VoidTransaction: %v", err)
	}
	txs, err := Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	for _, tx := range txs {
		if tx.ID == "aaaa1111cccc3333" {
			t.Error("the voided line should have left the books")
		}
	}
}

// Invoices take the same shorthand: a settle quoted by prefixes keys the settlement to the full
// invoice and transaction fingerprints.
func TestSettleInvoiceResolvesBothPrefixes(t *testing.T) {
	log := prefixLog(t)
	inv, added, err := Raise(log, "human", "", Invoice{
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Party: "J. Smith",
		Amount: model.Amount{Units: 6240, Scale: 2, Commodity: "CAD"}, Category: "Income:Consulting",
	})
	if err != nil || !added {
		t.Fatalf("Raise: added=%v err=%v", added, err)
	}

	if err := SettleInvoice(log, "human", inv.ID[:6], "aaaa1111b"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}
	settled, err := InvoiceSettlements(log)
	if err != nil {
		t.Fatalf("InvoiceSettlements: %v", err)
	}
	if settled[inv.ID] != "aaaa1111bbbb2222" {
		t.Errorf("settlements = %v, want the full ids on both sides", settled)
	}
}
