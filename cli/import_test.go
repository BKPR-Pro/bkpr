package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/store"
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

// -format says what a file is when its extension does not: hand-kept books in a .txt file import
// as a ledger without renaming, and the label still carries the file's own name.
func TestImportFormatFlagReadsLedgerRegardlessOfExtension(t *testing.T) {
	bookHere(t)
	path := filepath.Join(".", "accounting.txt")
	if err := os.WriteFile(path, []byte("2026/03/01  * Acme Hardware\n  Expenses:Materials  84.20 CAD\n  Assets:Bank:Chequing\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := importCmd([]string{path, "-format", "ledger"}); err != nil {
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
	events, err := s.Log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(events) == 0 || events[0].Actor != "statement:accounting.txt" {
		t.Fatalf("recorded actor %q, want statement:accounting.txt", events[0].Actor)
	}
}

// The flag package's = spelling is the same assertion.
func TestImportFormatEqualsSpellingIsAccepted(t *testing.T) {
	bookHere(t)
	path := filepath.Join(".", "accounting.txt")
	if err := os.WriteFile(path, []byte("2026/03/01  * Acme Hardware\n  Expenses:Materials  84.20 CAD\n  Assets:Bank:Chequing\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := importCmd([]string{path, "-format=ledger"}); err != nil {
		t.Fatalf("import: %v", err)
	}
}

// A -format nobody reads is refused by name, not mistaken for a connector.
func TestImportUnknownFormatIsRefused(t *testing.T) {
	bookHere(t)
	path := filepath.Join(".", "accounting.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := importCmd([]string{path, "-format", "xml"})
	if err == nil || !strings.Contains(err.Error(), `"xml"`) || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("err = %v, want a refusal naming xml and the supported formats", err)
	}
}

// -format at the end of the flags still needs its value.
func TestImportFormatWithoutValueIsRefused(t *testing.T) {
	bookHere(t)

	err := importCmd([]string{"accounting.txt", "-format"})
	if err == nil || !strings.Contains(err.Error(), "-format") {
		t.Fatalf("err = %v, want a refusal naming -format", err)
	}
}
