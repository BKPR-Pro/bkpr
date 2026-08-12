package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
	"github.com/BKPR-Pro/bkpr/lib/store"
)

func on(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }

// raiseTestInvoice raises one open invoice in the book here and returns its fingerprint, the one
// invoice list prints.
func raiseTestInvoice(t *testing.T) string {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	inv, _, err := books.Raise(s.Log, "human", "", books.Invoice{
		Date: on(15), Party: "J. Smith", Amount: model.Amount{Units: 160000, Scale: 2, Commodity: "CAD"},
		Category: "Income:Consulting", Number: "2086",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return inv.ID
}

func cadTx(id string, cents int64) model.Transaction {
	return model.Transaction{ID: id, Account: "Assets:Bank:Chequing", Date: on(15), Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}}
}

// The biller is the account the money moved through, dressed from its metadata; the bill-to is the
// payee; the line items are the postings with the sign flipped so a payment reads as a charge.
func TestBuildInvoiceUsesAccountMetadataForTheLetterhead(t *testing.T) {
	tx := cadTx("abc123", 160000) // $1600 received
	entry := model.Entry{Payee: "J. Smith", Postings: []model.Posting{
		{Account: "Income:Rent:123 Main", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}},
	}}
	meta := map[string]map[string]string{
		"Assets:Bank:Chequing": {"name": "Northwind Studio", "address": "123 Main St\nOttawa ON"},
	}

	doc := buildInvoice(tx, entry, meta, "invoice")

	if doc.Title != "INVOICE" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.Biller.Name != "Northwind Studio" || len(doc.Biller.Address) != 2 {
		t.Errorf("biller = %+v", doc.Biller)
	}
	if doc.BillTo.Name != "J. Smith" {
		t.Errorf("bill-to = %+v", doc.BillTo)
	}
	if !doc.Paid || doc.Reference != "abc123" {
		t.Errorf("paid/ref = %v %q", doc.Paid, doc.Reference)
	}
	if len(doc.Items) != 1 || doc.Items[0].Amount != "1600.00 CAD" || doc.Items[0].Label != "123 Main" {
		t.Errorf("items = %+v", doc.Items)
	}
	if doc.Total != "1600.00 CAD" {
		t.Errorf("total = %q", doc.Total)
	}
}

// A note on a posting is the human description of that line item, so an invoice shows it in place of
// the bare account leaf. A person writing item descriptions as posting comments wants the invoice to
// read as those words, not as the account they were filed under. An uncommented leg still shows its
// leaf, so a split with only some items described stays readable.
func TestBuildInvoiceLabelsItemsWithThePostingNote(t *testing.T) {
	tx := cadTx("abc123", 160000) // $1600 received
	entry := model.Entry{Payee: "J. Smith", Postings: []model.Posting{
		{Account: "Income:Consulting", Amount: model.Amount{Units: -120000, Scale: 2, Commodity: "CAD"}, Comment: "Website redesign, phase 2"},
		{Account: "Income:Consulting:Hosting", Amount: model.Amount{Units: -40000, Scale: 2, Commodity: "CAD"}},
	}}

	doc := buildInvoice(tx, entry, nil, "invoice")

	if len(doc.Items) != 2 {
		t.Fatalf("items = %+v, want 2", doc.Items)
	}
	if doc.Items[0].Label != "Website redesign, phase 2" {
		t.Errorf("described item label = %q, want the posting note", doc.Items[0].Label)
	}
	if doc.Items[1].Label != "Hosting" {
		t.Errorf("uncommented item label = %q, want the account leaf", doc.Items[1].Label)
	}
}

// An account's own name metadata is its durable display name on a document, so a line item reads as
// that name rather than the account's leaf. A book naming its tax account HST gets HST on every
// invoice thereafter without writing a note on each posting.
func TestItemLabelPrefersTheAccountNameMetadataOverTheLeaf(t *testing.T) {
	meta := map[string]map[string]string{
		"Liabilities:Real Estate:HST:Rent:Unit A": {"name": "HST"},
	}
	p := model.Posting{Account: "Liabilities:Real Estate:HST:Rent:Unit A"}
	if got := itemLabel(p, meta); got != "HST" {
		t.Errorf("label = %q, want the account's name metadata", got)
	}
}

// A note on the posting describes this line on this document, so it wins over the account's standing
// name metadata.
func TestItemLabelPrefersThePostingNoteOverTheAccountName(t *testing.T) {
	meta := map[string]map[string]string{
		"Income:Real Estate:Rent:Unit A": {"name": "Rent"},
	}
	p := model.Posting{Account: "Income:Real Estate:Rent:Unit A", Comment: "August rent"}
	if got := itemLabel(p, meta); got != "August rent" {
		t.Errorf("label = %q, want the posting note", got)
	}
}

// With neither a note nor a name the leaf is still the label, so an unannotated book reads as it
// always did.
func TestItemLabelFallsBackToTheAccountLeaf(t *testing.T) {
	p := model.Posting{Account: "Income:Real Estate:Rent:Unit A"}
	if got := itemLabel(p, nil); got != "Unit A" {
		t.Errorf("label = %q, want the account leaf", got)
	}
}

// A taxed rent invoice folds into two postings whose accounts share a leaf; naming one of them gives
// the document two distinct lines, so the invoice can be sent at all.
func TestBuildInvoiceDistinguishesPostingsSharingALeaf(t *testing.T) {
	tx := cadTx("abc123", 226000)
	entry := model.Entry{Payee: "Commercial Tenant", Postings: []model.Posting{
		{Account: "Income:Real Estate:Rent:Unit A", Amount: model.Amount{Units: -200000, Scale: 2, Commodity: "CAD"}},
		{Account: "Liabilities:Real Estate:HST:Rent:Unit A", Amount: model.Amount{Units: -26000, Scale: 2, Commodity: "CAD"}},
	}}
	meta := map[string]map[string]string{
		"Liabilities:Real Estate:HST:Rent:Unit A": {"name": "HST"},
	}

	doc := buildInvoice(tx, entry, meta, "invoice")

	if len(doc.Items) != 2 {
		t.Fatalf("items = %+v, want 2", doc.Items)
	}
	if doc.Items[0].Label != "Unit A" {
		t.Errorf("rent label = %q, want the account leaf", doc.Items[0].Label)
	}
	if doc.Items[1].Label != "HST" {
		t.Errorf("tax label = %q, want the account's name metadata", doc.Items[1].Label)
	}
}

// Without metadata the biller falls back to the account path, so a document still renders.
func TestBuildInvoiceFallsBackToTheAccountPath(t *testing.T) {
	doc := buildInvoice(cadTx("x", 100), model.Entry{Payee: "Someone",
		Postings: []model.Posting{{Account: "Income:Misc", Amount: model.Amount{Units: -100, Scale: 2, Commodity: "CAD"}}}},
		nil, "receipt")
	if doc.Biller.Name != "Assets:Bank:Chequing" {
		t.Errorf("biller fallback = %q", doc.Biller.Name)
	}
	if doc.Title != "RECEIPT" {
		t.Errorf("title = %q", doc.Title)
	}
}

// A USD bill paid in CAD invoices in the currency that was billed: the line item and total are the
// USD owed, drawn from the posting's own amount rather than the CAD that happened to land.
func TestBuildInvoiceBillsInThePostingCurrency(t *testing.T) {
	tx := cadTx("fx", 137000) // $1370 CAD landed
	cost := model.Amount{Units: 137000, Scale: 2, Commodity: "CAD"}
	entry := model.Entry{Payee: "Acme Corp", Postings: []model.Posting{
		{Account: "Income:Consulting:Acme", Amount: model.Amount{Units: -1000, Commodity: "USD"}, Cost: &cost},
	}}

	doc := buildInvoice(tx, entry, nil, "invoice")

	if len(doc.Items) != 1 || doc.Items[0].Amount != "1000 USD" {
		t.Errorf("items = %+v, want the billed 1000 USD", doc.Items)
	}
	if doc.Total != "1000 USD" {
		t.Errorf("total = %q, want the billed 1000 USD", doc.Total)
	}
}

// The document renders as a self-contained HTML page carrying the fields a reader expects.
func TestRenderInvoiceEmitsHTML(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent:123 Main", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		map[string]map[string]string{"Assets:Bank:Chequing": {"name": "Northwind Studio"}}, "invoice")

	var buf bytes.Buffer
	if err := renderInvoiceHTML(&buf, doc); err != nil {
		t.Fatalf("renderInvoiceHTML: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html>", "INVOICE", "Northwind Studio", "J. Smith", "1600.00 CAD", "PAID", "abc123"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

// The document is set in a monospace face: that is how these invoices have always read, and figures
// only line up column-wise in mono. The proportional system stack must not come back.
func TestRenderInvoiceSetsTheDocumentInMonospace(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		nil, "invoice")

	var buf bytes.Buffer
	if err := renderInvoiceHTML(&buf, doc); err != nil {
		t.Fatalf("renderInvoiceHTML: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "font-family: ui-monospace") || !strings.Contains(html, "monospace;") {
		t.Errorf("rendered HTML declares no monospace family:\n%s", html)
	}
	for _, unwanted := range []string{"-apple-system", "sans-serif"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("rendered HTML still declares the proportional stack %q", unwanted)
		}
	}
}

// The text form is the default: the same fields, no markup.
func TestRenderInvoiceTextCarriesTheFields(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent:123 Main", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		map[string]map[string]string{"Assets:Bank:Chequing": {"name": "Northwind Studio"}}, "invoice")

	var buf bytes.Buffer
	if err := renderInvoiceText(&buf, doc); err != nil {
		t.Fatalf("renderInvoiceText: %v", err)
	}
	text := buf.String()
	if strings.Contains(text, "<") {
		t.Errorf("text output should carry no markup:\n%s", text)
	}
	for _, want := range []string{"INVOICE", "Northwind Studio", "Invoice No: abc123", "PAID", "J. Smith", "1600.00 CAD", "Total"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q", want)
		}
	}
}

// An entry the bank has cleared renders PAID: an ordinary imported line carries no pending flag, so
// the document reads as money that moved, exactly as it did before Paid was derived.
func TestBuildInvoiceMarksAClearedEntryPaid(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		nil, "invoice")
	if !doc.Paid {
		t.Errorf("paid = %v, want a cleared bank line to read PAID", doc.Paid)
	}
}

// A raised invoice whose cash has not arrived is pending, so the document must not claim it is paid.
func TestBuildInvoiceLeavesAPendingEntryUnpaid(t *testing.T) {
	doc := buildInvoice(cadTx("invoice:abc123", 160000), model.Entry{Payee: "J. Smith", Pending: true,
		Postings: []model.Posting{{Account: "Income:Rent", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		nil, "invoice")
	if doc.Paid {
		t.Errorf("paid = %v, want an unsettled accrual to read unpaid", doc.Paid)
	}
}

// The reference a reader quotes is the invoice number, not the fingerprint the fold keyed the line
// under, so a document raised as invoice 2086 reads "Invoice No: 2086".
func TestBuildInvoicePrefersTheInvoiceNumberAsTheReference(t *testing.T) {
	doc := buildInvoice(cadTx("invoice:4f3a", 160000), model.Entry{Payee: "J. Smith", Invoice: "2086",
		Postings: []model.Posting{{Account: "Income:Rent", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		nil, "invoice")
	if doc.Reference != "2086" {
		t.Errorf("reference = %q, want the invoice number", doc.Reference)
	}
}

// Without a number the fingerprint is still the reference, so an ordinary line keeps its identity on
// the document.
func TestBuildInvoiceFallsBackToTheFingerprintAsTheReference(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		nil, "invoice")
	if doc.Reference != "abc123" {
		t.Errorf("reference = %q, want the transaction fingerprint", doc.Reference)
	}
}

// An account carrying an address but no name heads the letterhead with the address's first line,
// since that line is the name a person wrote there; the rest stays the address. Otherwise the
// document would be headed by a raw account path.
func TestPartyForNamesTheLetterheadFromTheAddressWhenThereIsNoName(t *testing.T) {
	p := partyFor("Assets:Consulting:Chequing", "Assets:Consulting:Chequing", map[string]map[string]string{
		"Assets:Consulting:Chequing": {"address": "Northwind Studio\n123 Main St\nOttawa ON"},
	})
	if p.Name != "Northwind Studio" {
		t.Errorf("name = %q, want the address's first line", p.Name)
	}
	if len(p.Address) != 2 || p.Address[0] != "123 Main St" || p.Address[1] != "Ottawa ON" {
		t.Errorf("address = %+v, want the remaining lines", p.Address)
	}
}

// A name in the metadata still wins, and the whole address stays the address.
func TestPartyForPrefersTheNameMetadata(t *testing.T) {
	p := partyFor("Assets:Bank:Chequing", "fallback", map[string]map[string]string{
		"Assets:Bank:Chequing": {"name": "Northwind Studio", "address": "123 Main St\nOttawa ON"},
	})
	if p.Name != "Northwind Studio" {
		t.Errorf("name = %q", p.Name)
	}
	if len(p.Address) != 2 || p.Address[0] != "123 Main St" {
		t.Errorf("address = %+v, want both address lines", p.Address)
	}
}

// receipt reads the accrual basis, so an invoice raised but not yet paid has a document at all; and
// -tx takes the bare fingerprint invoice list printed, not the fold's namespaced id.
func TestReceiptRendersARaisedInvoiceByItsBareFingerprint(t *testing.T) {
	bookHere(t)
	id := raiseTestInvoice(t)

	out, err := withPipedStdout(t, func() error { return receiptCmd([]string{"-tx", id, "-as", "invoice"}) })
	if err != nil {
		t.Fatalf("receiptCmd: %v", err)
	}
	var doc invoiceDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output was not JSON: %v\n%s", err, out)
	}
	if doc.Paid {
		t.Errorf("paid = %v, want an unsettled raised invoice to read unpaid", doc.Paid)
	}
	if doc.Reference != "2086" {
		t.Errorf("reference = %q, want the invoice number", doc.Reference)
	}
}

// Off a terminal, receipt defaults to json rather than the text a person printing a document wants.
func TestReceiptDefaultsToJSONWhenStdoutIsNotATerminal(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Income:Rent"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	out, err := withPipedStdout(t, func() error { return receiptCmd([]string{"-tx", "tx1"}) })
	if err != nil {
		t.Fatalf("receiptCmd: %v", err)
	}
	var parsed struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output was not JSON: %v\n%s", err, out)
	}
	if parsed.Title == "" {
		t.Errorf("parsed = %+v, want a title", parsed)
	}
}
