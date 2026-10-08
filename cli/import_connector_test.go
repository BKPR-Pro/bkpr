package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/adapters/source"
	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
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
		fetch, err := fetcherFor(kind, fetchOpts{})
		if err != nil || fetch == nil {
			t.Errorf("%s should have an importer: fetch=%v err=%v", kind, fetch, err)
		}
	}
}

// rentapp is the export direction; importing from it is refused with a message that says why, not a
// generic failure.
func TestFetcherForRentappIsExportOnly(t *testing.T) {
	_, err := fetcherFor("rentapp", fetchOpts{})
	if err == nil {
		t.Fatal("rentapp is export-only; importing from it should be refused")
	}
	if !strings.Contains(err.Error(), "export-only") {
		t.Errorf("the error should say rentapp is export-only: %v", err)
	}
}

// A kind with no importer is a clear error rather than a silent no-op import.
func TestFetcherForUnknownKind(t *testing.T) {
	if _, err := fetcherFor("mystery", fetchOpts{}); err == nil {
		t.Fatal("an unknown connector kind should have no importer")
	}
}

// With no -history/-from, the default window starts at the account's last bank-balance check less a
// few days' overlap, so lines dated between two runs are never skipped. An explicit window, or a
// first-ever import, is left alone.
func TestImportWindowReachesBackToTheLastBalanceCheck(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	c := books.Connector{Name: "chq", Account: "Assets:Chequing"}
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

	// First-ever import: nothing recorded, the default stays.
	o, note := importWindow(log, c, fetchOpts{}, now)
	if o.from != "" || o.to != "" {
		t.Fatalf("a first import should keep the default window, got from=%q to=%q", o.from, o.to)
	}
	if note != "reading the site's default window" {
		t.Errorf("note = %q", note)
	}

	books.AssertBalance(log, "t", c.Account, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), model.Amount{Units: 1, Scale: 2, Commodity: "CAD"})
	o, note = importWindow(log, c, fetchOpts{}, now)
	if o.from != "Aug 17, 2026" || o.to != "Sep 12, 2026" {
		t.Errorf("window = %q..%q, want Aug 17, 2026..Sep 12, 2026", o.from, o.to)
	}
	if note != "reading from 2026-08-17 to 2026-09-12 (last bank balance 2026-08-20)" {
		t.Errorf("note = %q", note)
	}

	// A recent check still sets the window: the site's short default is not trusted to span it.
	books.AssertBalance(log, "t", c.Account, now.AddDate(0, 0, -5), model.Amount{Units: 1, Scale: 2, Commodity: "CAD"})
	if o, _ = importWindow(log, c, fetchOpts{}, now); o.from != "Sep 4, 2026" {
		t.Errorf("a check 5 days ago should read from 8 days ago; got from=%q", o.from)
	}

	// Explicit flags are untouched.
	books.AssertBalance(log, "t", c.Account, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), model.Amount{Units: 1, Scale: 2, Commodity: "CAD"})
	if o, _ = importWindow(log, c, fetchOpts{history: 10}, now); o.from != "" || o.history != 10 {
		t.Errorf("-history should be kept as is, got %+v", o)
	}
	if o, _ = importWindow(log, c, fetchOpts{from: "Feb 1, 2026", to: "Mar 1, 2026"}, now); o.from != "Feb 1, 2026" || o.to != "Mar 1, 2026" {
		t.Errorf("-from/-to should be kept as is, got %+v", o)
	}
}

// The default window must reach the bank script, not only the printed note. importConnector once built
// the fetch before settling the window, so the script read the site's short default while the note
// claimed the window from the last balance check; a month's lines went missing twice that way.
func TestImportConnectorSendsTheDefaultWindowToTheBank(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	c := books.Connector{Name: "chq", Kind: "rbc", Account: "Assets:Chequing"}
	books.AssertBalance(log, "t", c.Account, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), model.Amount{Units: 1, Scale: 2, Commodity: "CAD"})

	var sent source.Bank
	orig := readBank
	readBank = func(b source.Bank) (source.BankResult, error) { sent = b; return source.BankResult{}, nil }
	defer func() { readBank = orig }()

	if err := importConnector(log, c, fetchOpts{}); err != nil {
		t.Fatalf("importConnector: %v", err)
	}
	if sent.HistoryFrom != "Sep 9, 2026" || sent.HistoryTo == "" {
		t.Errorf("the bank got from=%q to=%q; want from=\"Sep 9, 2026\" and a to date", sent.HistoryFrom, sent.HistoryTo)
	}
}
