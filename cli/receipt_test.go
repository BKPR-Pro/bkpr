package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bkpr/lib/model"
)

func on(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }

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
		"Assets:Bank:Chequing": {"name": "Excite Creative", "address": "123 Main St\nOttawa ON"},
	}

	doc := buildInvoice(tx, entry, meta, "invoice")

	if doc.Title != "INVOICE" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.Biller.Name != "Excite Creative" || len(doc.Biller.Address) != 2 {
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
		map[string]map[string]string{"Assets:Bank:Chequing": {"name": "Excite Creative"}}, "invoice")

	var buf bytes.Buffer
	if err := renderInvoiceHTML(&buf, doc); err != nil {
		t.Fatalf("renderInvoiceHTML: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html>", "INVOICE", "Excite Creative", "J. Smith", "1600.00 CAD", "PAID", "abc123"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

// The text form is the default: the same fields, no markup.
func TestRenderInvoiceTextCarriesTheFields(t *testing.T) {
	doc := buildInvoice(cadTx("abc123", 160000), model.Entry{Payee: "J. Smith",
		Postings: []model.Posting{{Account: "Income:Rent:123 Main", Amount: model.Amount{Units: -160000, Scale: 2, Commodity: "CAD"}}}},
		map[string]map[string]string{"Assets:Bank:Chequing": {"name": "Excite Creative"}}, "invoice")

	var buf bytes.Buffer
	if err := renderInvoiceText(&buf, doc); err != nil {
		t.Fatalf("renderInvoiceText: %v", err)
	}
	text := buf.String()
	if strings.Contains(text, "<") {
		t.Errorf("text output should carry no markup:\n%s", text)
	}
	for _, want := range []string{"INVOICE", "Excite Creative", "Invoice No: abc123", "PAID", "J. Smith", "1600.00 CAD", "Total"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q", want)
		}
	}
}
