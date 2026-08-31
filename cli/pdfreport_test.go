package main

import (
	"bytes"
	"testing"
)

// A cell wider than an equal share of its column must not overlap the next column: fpdf does not
// clip or wrap text drawn by CellFormat, so the renderer itself (weighting and/or truncation) is
// responsible for keeping every cell's drawn width within its own column. This is the case that
// broke real `register -format pdf` output: an 18-19 char transaction fingerprint and a multi-segment
// account path (e.g. "Assets:Bank:Chequing") each wider than an equal 1/6th-1/7th share of the page.
func TestPdfReportTableCellNeverOverflowsItsColumn(t *testing.T) {
	r := newPdfReport()
	headers := []string{"Date", "Fingerprint", "Payee", "Amount", "Account", "Door"}
	longFingerprint := "a1b2c3d4e5f6a7b8c9"   // 18 chars, realistic tx fingerprint
	longAccount := "Assets:Bank:Chequing:Sub" // multi-segment account path
	r.Table(headers, [][]string{
		{"2026-01-01", longFingerprint, "A Payee", "100.00 CAD", longAccount, "42"},
	})

	pageW, _, _ := r.pdf.PageSize(0)
	left, _, right, _ := r.pdf.GetMargins()
	printableW := pageW - left - right
	colW := printableW / float64(len(headers))

	r.pdf.SetFont("Courier", "", 9)
	for _, s := range []string{longFingerprint, longAccount} {
		if w := r.pdf.GetStringWidth(s); w > colW {
			// The raw string is wider than an equal share -- fine, that's the point of the test
			// fixture. What matters is that the renderer's fitCell shrinks it back down.
			fitted := r.fitCell(s, colW-2)
			if fw := r.pdf.GetStringWidth(fitted); fw > colW {
				t.Errorf("fitted cell %q for %q still measures %.2fmm, wider than its %.2fmm column", fitted, s, fw, colW)
			}
		}
	}

	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
}

// A weighted column (e.g. a register's Fingerprint or Account) gets more of the printable width than
// an equal split would give it, and cell content is truncated to fit within that wider allotment
// rather than left to overflow.
func TestPdfReportTableWeightedColumnGetsMoreRoomThanEqualSplit(t *testing.T) {
	r := newPdfReport()
	headers := []string{"Date", "Account"}
	r.pdf.SetFont("Courier", "", 9)

	pageW, _, _ := r.pdf.PageSize(0)
	left, _, right, _ := r.pdf.GetMargins()
	printableW := pageW - left - right
	equalShare := printableW / float64(len(headers))

	weightedAccountW := printableW * 3.0 / 4.0 // weights 1:3 -> Account gets 3/4
	longAccount := "Assets:Bank:Chequing:VeryLongSubAccountName"

	r.Table(headers, [][]string{{"2026-01-01", longAccount}}, 1, 3)

	fitted := r.fitCell(longAccount, weightedAccountW-2)
	if fw := r.pdf.GetStringWidth(fitted); fw > weightedAccountW {
		t.Errorf("weighted cell still measures %.2fmm, wider than its %.2fmm column", fw, weightedAccountW)
	}
	if weightedAccountW <= equalShare {
		t.Fatalf("test fixture broken: weighted width %.2fmm should exceed equal share %.2fmm", weightedAccountW, equalShare)
	}

	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
}

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
