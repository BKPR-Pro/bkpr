package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/store"
)

// End to end: import dispatches a .ledger file to the parser and records its reconstructed lines.
func TestImportLedgerFileImportsItsEntries(t *testing.T) {
	bookHere(t)
	path := filepath.Join(".", "march.ledger")
	if err := os.WriteFile(path, []byte("2026/03/01  * Acme Hardware\n  Expenses:Materials  84.20 CAD\n  Assets:Bank:Chequing\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := importCmd([]string{path}); err != nil {
		t.Fatalf("import: %v", err)
	}

	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	txs, err := books.Transactions(s.Log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 || txs[0].Account != "Assets:Bank:Chequing" || txs[0].Amount.String() != "-84.20 CAD" {
		t.Fatalf("imported %+v, want one Chequing line of -84.20 CAD", txs)
	}
}
