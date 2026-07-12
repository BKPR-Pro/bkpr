package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
)

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
`))
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
