package source_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/ledger"
	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/model"
)

// A comment left on one leg of a split must survive the writer and the reader: it renders inline,
// reads back onto the same posting, and re-renders byte-identically. This is what makes a note a
// person leaves on their books durable across an export/import round trip.
func TestLedgerPostingCommentRoundTrips(t *testing.T) {
	cad := func(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }
	date, err := time.Parse("2006/01/02", "2026/03/02")
	if err != nil {
		t.Fatalf("date: %v", err)
	}

	txs := []model.Transaction{{
		ID: "c-1", Account: "Assets:Bank:Chequing", Date: date, Amount: cad(-8420), Description: "Acme Hardware",
	}}
	entries := []model.Entry{{Payee: "Acme Hardware", Postings: []model.Posting{
		{Account: "Expenses:Repairs:Materials", Amount: cad(6000), Comment: "lumber for the deck"},
		{Account: "Expenses:Repairs:Tools", Amount: cad(2420)},
	}}}

	var first bytes.Buffer
	if err := ledger.WriteAll(&first, txs, entries); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	_, gotEntries, err := source.ReadLedger(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	if len(gotEntries) != 1 || len(gotEntries[0].Postings) != 2 {
		t.Fatalf("read back %+v, want one entry with two postings", gotEntries)
	}
	if c := gotEntries[0].Postings[0].Comment; c != "lumber for the deck" {
		t.Errorf("commented leg came back with %q, want the note carried", c)
	}
	if c := gotEntries[0].Postings[1].Comment; c != "" {
		t.Errorf("uncommented leg came back with %q, want no note", c)
	}

	var second bytes.Buffer
	if err := ledger.WriteAll(&second, txs, gotEntries); err != nil {
		t.Fatalf("re-render failed: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("comment round trip is not byte-identical:\n--- wrote ---\n%s\n--- re-rendered ---\n%s", first.String(), second.String())
	}
}

// A cost-basis line the ledger writer emits must read back through the source reader and re-render
// byte-identically. The reader's own @@ tests hand-author their ledger text, so nothing ties the
// writer's actual output to the reader; this drives real ledger.WriteAll bytes through ReadLedger.
// Two writer shapes are covered: a fully-priced share buy (its cash leg elided) and a property buy
// whose two Property legs cancel in cash alongside a same-commodity split.
func TestLedgerCostBasisRoundTripsThroughTheWriter(t *testing.T) {
	usd := func(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }
	cad := func(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }
	amt := func(a model.Amount) *model.Amount { return &a }
	date := func(s string) time.Time {
		d, err := time.Parse("2006/01/02", s)
		if err != nil {
			t.Fatalf("date %q: %v", s, err)
		}
		return d
	}

	txs := []model.Transaction{
		// A share buy: the cash leg is elided, the share leg carries its @@ cost.
		{
			ID: "buy-1", Account: "Assets:Bank", Date: date("2026/03/01"),
			Amount: usd(-100000), Description: "BUY AAPL",
		},
		// A property buy: the two Property legs cancel in cash, a legal fee shares the CAD line, and
		// the financing (a LOC) is the elided plug.
		{
			ID: "buy-2", Account: "Liabilities:Simplii LOC:9 Schoodic Street", Date: date("2024/10/10"),
			Amount: cad(-500000), Description: "9 Schoodic Street",
		},
	}
	entries := []model.Entry{
		{Payee: "Broker", Postings: []model.Posting{
			{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: amt(usd(100000))},
		}},
		{Payee: "Vendor", Postings: []model.Posting{
			{Account: "Equity:Real Estate:9 Schoodic Street", Amount: model.Amount{Units: -1, Commodity: "Property"}, Cost: amt(cad(5000000))},
			{Account: "Assets:Real Estate:9 Schoodic Street", Amount: model.Amount{Units: 1, Commodity: "Property"}, Cost: amt(cad(5000000))},
			{Account: "Expenses:Real Estate:Legal:9 Schoodic Street", Amount: cad(500000)},
		}},
	}

	var first bytes.Buffer
	if err := ledger.WriteAll(&first, txs, entries); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	gotTxs, gotEntries, err := source.ReadLedger(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatalf("re-import of the writer's own output failed: %v", err)
	}
	if len(gotTxs) != len(txs) {
		t.Fatalf("read back %d lines, wrote %d", len(gotTxs), len(txs))
	}

	// The cost survives the trip: the share leg and both property legs carry their @@ price back, and
	// each reconstructed entry accounts for its whole line.
	if c := gotEntries[0].Postings[0].Cost; c == nil || c.String() != "1000.00 USD" {
		t.Errorf("share leg cost = %v, want 1000.00 USD carried back", c)
	}
	for i, tx := range gotTxs {
		if !gotEntries[i].Balances(tx) {
			t.Errorf("re-imported entry %d does not balance its line", i)
		}
		if gotEntries[i].Uncategorized() {
			t.Errorf("re-imported entry %d collapsed a priced leg into Uncategorized", i)
		}
	}

	// The whole point of a round trip: rendering the re-imported books reproduces the writer's bytes
	// exactly. A drift in either the writer's @@ form or the reader's parse of it breaks this.
	var second bytes.Buffer
	if err := ledger.WriteAll(&second, gotTxs, gotEntries); err != nil {
		t.Fatalf("re-render of the re-imported books failed: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("round trip is not byte-identical:\n--- wrote ---\n%s\n--- re-rendered ---\n%s", first.String(), second.String())
	}
}
