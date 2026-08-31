package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

// The PDF form of accounts due is a real PDF, carrying the same rows printDue's tabwriter prints.
func TestRenderDuePDFStartsWithThePDFSignature(t *testing.T) {
	rows := []books.DueAccount{
		{Account: "Liabilities:Acme Card", Balance: map[string]model.Amount{"CAD": cad2(8420)}, Due: "2026-04-01", Minimum: "25.00 CAD"},
	}

	var buf bytes.Buffer
	if err := renderDuePDF(&buf, rows); err != nil {
		t.Fatalf("renderDuePDF: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Errorf("renderDuePDF did not emit a PDF: %q", buf.Bytes()[:20])
	}
}

// An empty due list must still render a valid PDF rather than erroring, matching printDue's own
// no-rows message.
func TestRenderDuePDFWithNoRowsStillOutputsAValidPDF(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDuePDF(&buf, nil); err != nil {
		t.Fatalf("renderDuePDF: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Error("an empty due list should still render a valid PDF")
	}
}

// PDF is binary, so -format pdf without -out is refused with a clear error, exactly as receipt and
// register refuse it.
func TestAccountDueRequiresOutWhenFormatIsPDF(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	err := accountDue([]string{"-format", "pdf"})
	if err == nil {
		t.Fatal("accountDue should refuse -format pdf without -out")
	}
	if !strings.Contains(err.Error(), "-out") {
		t.Errorf("error = %q, want it to mention -out", err.Error())
	}
}

// With -out set, -format pdf writes a real PDF file.
func TestAccountDueWritesAPDFFileWhenFormatIsPDFWithOut(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	dir := t.TempDir()
	out := dir + "/due.pdf"
	if err := accountDue([]string{"-format", "pdf", "-out", out}); err != nil {
		t.Fatalf("accountDue: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Errorf("written file is not a PDF: %q", data[:20])
	}
}

// -format still rejects anything outside table, json, and pdf.
func TestAccountDueRejectsAnUnknownFormat(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := accountDue([]string{"-format", "csv"}); err == nil {
		t.Error("accountDue should refuse an unknown -format")
	}
}
