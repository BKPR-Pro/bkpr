package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

func fakeTx(id string, cents int64) model.Transaction {
	return model.Transaction{
		ID: id, Account: "Liabilities:Card:RBC Mastercard",
		Date:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"},
	}
}

// Every input flows through importFrom: a Source's lines land in the log the same way, whether the
// source is a file reader or a connector fetch. A fake source stands in for both.
func TestImportFromRecordsWhatTheSourceYields(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	src := func() ([]model.Transaction, error) {
		return []model.Transaction{fakeTx("a", -1234), fakeTx("b", -5678)}, nil
	}

	if err := importFrom(log, "connector:rbc-mc", src); err != nil {
		t.Fatalf("importFrom: %v", err)
	}

	txs, err := books.Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("recorded %d transactions, want 2", len(txs))
	}
}

// Importing is idempotent through the fingerprint, so running the same source twice is a no-op the
// second time, the same guarantee a re-imported CSV has.
func TestImportFromIsIdempotent(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	src := func() ([]model.Transaction, error) {
		return []model.Transaction{fakeTx("a", -1234)}, nil
	}

	importFrom(log, "x", src)
	if err := importFrom(log, "x", src); err != nil {
		t.Fatalf("second import: %v", err)
	}

	txs, _ := books.Transactions(log)
	if len(txs) != 1 {
		t.Errorf("a repeated source recorded %d transactions, want the one", len(txs))
	}
}

// A source that fails (a login that did not take, a file that vanished) stops the import rather than
// recording an empty or partial statement.
func TestImportFromPropagatesSourceError(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	src := func() ([]model.Transaction, error) { return nil, errFetch }

	if err := importFrom(log, "x", src); err == nil {
		t.Fatal("a failed source should stop the import")
	}
	if txs, _ := books.Transactions(log); len(txs) != 0 {
		t.Errorf("a failed source recorded %d transactions, want none", len(txs))
	}
}

type fetchErr struct{}

func (fetchErr) Error() string { return "fetch failed" }

var errFetch = fetchErr{}

// A bank kind has an importer wired, so `import <name>` reaches its fetch.
func TestFetcherForBankIsWired(t *testing.T) {
	for _, kind := range []string{"rbc", "simplii", "pcfinancial"} {
		fetch, err := fetcherFor(kind)
		if err != nil || fetch == nil {
			t.Errorf("%s should have an importer: fetch=%v err=%v", kind, fetch, err)
		}
	}
}

// rentapp is the export direction; importing from it is refused with a message that says why, not a
// generic failure.
func TestFetcherForRentappIsExportOnly(t *testing.T) {
	_, err := fetcherFor("rentapp")
	if err == nil {
		t.Fatal("rentapp is export-only; importing from it should be refused")
	}
	if !strings.Contains(err.Error(), "export-only") {
		t.Errorf("the error should say rentapp is export-only: %v", err)
	}
}

// A kind with no importer is a clear error rather than a silent no-op import.
func TestFetcherForUnknownKind(t *testing.T) {
	if _, err := fetcherFor("mystery"); err == nil {
		t.Fatal("an unknown connector kind should have no importer")
	}
}
