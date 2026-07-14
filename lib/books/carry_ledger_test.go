package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
)

// A standalone note a person kept inside an entry must survive the whole import path: the reader
// takes it off the file, CarryCategorizations records it, and the fold hands it back on the entry.
// Without threading it through the categorization event, the note is read but never stored, so it
// vanishes the moment the line is folded from the log.
func TestCarryingKeepsAnEntrysBlockComments(t *testing.T) {
	log := newLog()

	txs, entries, err := source.ReadLedger(strings.NewReader(`2025/09/13  * Dallas Read
  ; Sephora          238.05 CAD
  ; Store             55.78 CAD
  Expenses:Discretionary  1000.00 CAD
  Assets:Bank:Chequing
`), "books.ledger")
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if _, err := books.Import(log, "import:acct.txt", txs); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, _, err := books.CarryCategorizations(log, "import:acct.txt", "from acct.txt", txs, entries); err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}

	_, folded := ledger(t, log)
	if len(folded) != 1 {
		t.Fatalf("folded %d entries, want 1", len(folded))
	}
	got := folded[0].BlockComments
	if len(got) != 2 || got[0] != "Sephora          238.05 CAD" || got[1] != "Store             55.78 CAD" {
		t.Fatalf("block comments lost across the log: %q", got)
	}
}

// An invoice or bill number the file names as a (code) must survive the whole import path: the
// reader lifts it off the header, CarryCategorizations records it, and the fold hands it back on the
// entry. Without threading it through the categorization event it is read but never stored, so the
// number vanishes the moment the line is folded from the log.
func TestCarryingKeepsAnEntrysInvoiceNumber(t *testing.T) {
	log := newLog()

	txs, entries, err := source.ReadLedger(strings.NewReader(`2026/04/01  * (2073) DNSimple
  Income:Consulting:Contract:DNSimple  -9000.00 USD
  Assets:Consulting:Chequing
`), "books.ledger")
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if _, err := books.Import(log, "import:acct.txt", txs); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, _, err := books.CarryCategorizations(log, "import:acct.txt", "from acct.txt", txs, entries); err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}

	_, folded := ledger(t, log)
	if len(folded) != 1 {
		t.Fatalf("folded %d entries, want 1", len(folded))
	}
	if folded[0].Invoice != "2073" {
		t.Fatalf("invoice number lost across the log: %q", folded[0].Invoice)
	}
	if folded[0].Payee != "DNSimple" {
		t.Fatalf("payee = %q, want the code lifted off the name", folded[0].Payee)
	}
}

// The whole point of carrying: a hand-kept ledger file already names its accounts, so importing it
// and folding shows those accounts with no rule loaded and no manual categorize per line. This is
// the end-to-end path the reader and the CarryCategorizations command exist to make honest.
func TestImportingACategorizedLedgerNeedsNoManualCategorize(t *testing.T) {
	log := newLog() // no rules at all: without the carry, every line falls to Uncategorized

	txs, entries, err := source.ReadLedger(strings.NewReader(`2026/03/01  * Acme Hardware
  Expenses:Materials  84.20 CAD
  Assets:Bank:Chequing

2026/03/03  * Rent
  Income:Rent  -168.00 CAD
  Assets:Bank:Chequing
`), "books.ledger")
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if _, err := books.Import(log, "import:acct.txt", txs); err != nil {
		t.Fatalf("Import: %v", err)
	}
	carried, skipped, err := books.CarryCategorizations(log, "import:acct.txt", "from acct.txt", txs, entries)
	if err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}
	if carried != 2 || skipped != 0 {
		t.Fatalf("got carried=%d skipped=%d, want 2 and 0", carried, skipped)
	}

	_, entriesFolded := ledger(t, log)
	got := map[string]bool{}
	for _, e := range entriesFolded {
		got[e.Postings[0].Account] = true
	}
	for _, want := range []string{"Expenses:Materials", "Income:Rent"} {
		if !got[want] {
			t.Errorf("folded books missing %q; the file's categorization was not carried", want)
		}
	}
	for _, e := range entriesFolded {
		if e.Uncategorized() {
			t.Errorf("a line folded to Uncategorized despite the file naming its account: %+v", e)
		}
	}
}
