package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dallasread/bkpr/lib/store"
)

// reset is the one command that destroys history, so without -confirm it must touch nothing.
func TestResetIsADryRunWithoutConfirm(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	if err := resetCmd(nil); err != nil {
		t.Fatalf("reset dry run: %v", err)
	}
	txs, _, _ := foldStore(t)
	if len(txs) != 1 {
		t.Fatalf("a dry run discarded the books: %d lines left", len(txs))
	}
}

// -confirm empties the book: no events, no artifact, but still a book — init is not needed again,
// and the old log is recoverable only from git.
func TestResetConfirmEmptiesTheBook(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := renderBooks([]string{"-format", "ledger"}); err != nil {
		t.Fatalf("render: %v", err)
	}

	if err := resetCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("reset -confirm: %v", err)
	}

	txs, _, _ := foldStore(t)
	if len(txs) != 0 {
		t.Fatalf("the book still holds %d lines after a reset", len(txs))
	}
	if _, err := os.Stat(filepath.Join(store.Dir, store.LedgerFile)); !os.IsNotExist(err) {
		t.Error("the artifact should be gone after a reset")
	}
	// Still a book: the next import needs no init.
	seedTx(t, "tx2")
	if txs, _, _ := foldStore(t); len(txs) != 1 {
		t.Error("an emptied book should accept new facts without another init")
	}
}

func TestResetOnAnEmptyBookIsANoOp(t *testing.T) {
	bookHere(t)
	if err := resetCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("reset on an empty book: %v", err)
	}
}
