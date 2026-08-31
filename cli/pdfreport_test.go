package main

import (
	"bytes"
	"testing"
)

// Output must be a real PDF: the format is identified by its signature at the very start of the
// file, so a viewer (or a test) can tell a bkpr export apart from a truncated or wrong-typed file.
func TestPdfReportOutputStartsWithThePDFSignature(t *testing.T) {
	r := newPdfReport()
	r.Title("INVOICE")
	r.Line("Date: March 15, 2026")
	r.Table([]string{"Item", "Amount"}, [][]string{{"Rent", "1600.00 CAD"}})

	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Errorf("output does not start with the PDF signature: %q", buf.Bytes()[:min(20, buf.Len())])
	}
}

// A blank report (no title, no rows) must still produce a valid, openable PDF rather than erroring,
// since some documents render with nothing to show (e.g. no accounts due).
func TestPdfReportWithNoContentStillOutputsAValidPDF(t *testing.T) {
	r := newPdfReport()
	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Error("an empty report should still be a valid PDF")
	}
}

// A malformed table (a row with the wrong number of cells) is an fpdf-internal error state; Output
// must surface it rather than silently emitting a broken document.
func TestPdfReportSurfacesAnInternalError(t *testing.T) {
	r := newPdfReport()
	r.Table([]string{"A", "B"}, [][]string{{"only one cell"}})
	var buf bytes.Buffer
	if err := r.Output(&buf); err == nil {
		t.Error("Output should error on a malformed table rather than emit a broken PDF")
	}
}

// Title, Line, and Table must not panic when combined, and must produce a document large enough to
// actually carry the content (fpdf compresses streams, so the raw text is not asserted here).
func TestPdfReportTitleLineAndTableCombine(t *testing.T) {
	r := newPdfReport()
	r.Title("RECEIPT")
	r.Line("Bill To: J. Smith")
	r.Table([]string{"Item", "Amount"}, [][]string{{"Rent", "1600.00 CAD"}})
	r.Line("Total: 1600.00 CAD")

	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if buf.Len() < 512 {
		t.Errorf("expected a substantial PDF body, got %d bytes", buf.Len())
	}
}
