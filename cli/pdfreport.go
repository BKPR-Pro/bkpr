package main

import (
	"fmt"
	"io"

	"github.com/go-pdf/fpdf"
)

// pdfReport is the shared layout every PDF export (invoice, register, accounts due) builds on: a
// letterhead-style title, plain text lines, and a monospace-ish table with a header row -- the same
// three primitives the text and HTML renderers already lean on, just aimed at a page instead of a
// terminal or a browser.
type pdfReport struct {
	pdf *fpdf.Fpdf
}

// newPdfReport starts a single-page-growing A4 document with page numbers in the footer, margins
// tight enough to fit a register's wide rows.
func newPdfReport() *pdfReport {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 12, 12)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont("Courier", "", 8)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetFont("Courier", "", 10)
	return &pdfReport{pdf: pdf}
}

// Title sets the document's banner line, bold and a couple of sizes up -- the letterhead a printed
// invoice, statement, or report opens with.
func (r *pdfReport) Title(s string) {
	r.pdf.SetFont("Courier", "B", 16)
	r.pdf.CellFormat(0, 10, s, "", 1, "L", false, 0, "")
	r.pdf.SetFont("Courier", "", 10)
	r.pdf.Ln(2)
}

// Line writes one line of plain body text, e.g. a reference, a date, or a totals line.
func (r *pdfReport) Line(s string) {
	r.pdf.CellFormat(0, 6, s, "", 1, "L", false, 0, "")
}

// Table writes a header row and its data rows. Columns split the printable width proportionally to
// the given weights (one per header; a missing or non-positive weight falls back to 1, i.e. an equal
// share) -- callers with a column that reliably needs more room, e.g. a register's Fingerprint and
// Account or an invoice's Item, hand in a larger weight for it. Whatever still does not fit its
// column after that is truncated with a trailing ellipsis so no cell's drawn content can ever overlap
// the next column; nothing here claims to be a general table layout engine.
func (r *pdfReport) Table(headers []string, rows [][]string, weights ...float64) {
	if len(headers) == 0 {
		return
	}
	pageW, _, _ := r.pdf.PageSize(0)
	left, _, right, _ := r.pdf.GetMargins()
	printableW := pageW - left - right

	colWeights := make([]float64, len(headers))
	totalWeight := 0.0
	for i := range headers {
		w := 1.0
		if i < len(weights) && weights[i] > 0 {
			w = weights[i]
		}
		colWeights[i] = w
		totalWeight += w
	}
	colW := make([]float64, len(headers))
	for i, w := range colWeights {
		colW[i] = printableW * w / totalWeight
	}

	const cellPad = 2 // mm of left+right padding fpdf's CellFormat leaves inside a cell

	r.pdf.SetFont("Courier", "B", 8)
	for i, h := range headers {
		r.pdf.CellFormat(colW[i], 7, r.fitCell(h, colW[i]-cellPad), "B", 0, "L", false, 0, "")
	}
	r.pdf.Ln(-1)

	r.pdf.SetFont("Courier", "", 8)
	for _, row := range rows {
		if len(row) != len(headers) {
			r.pdf.SetErrorf("pdf table row has %d cell(s), want %d to match the header", len(row), len(headers))
			return
		}
		for i, cell := range row {
			r.pdf.CellFormat(colW[i], 6, r.fitCell(cell, colW[i]-cellPad), "", 0, "L", false, 0, "")
		}
		r.pdf.Ln(-1)
	}
	r.pdf.Ln(3)
}

// fitCell truncates s with a trailing ellipsis, character by character, until its rendered width
// (in the current font) fits within maxW -- so a cell too wide for its column shrinks instead of
// spilling into the next one. s is returned unchanged when it already fits. The ellipsis is plain
// ASCII ("...") rather than the single-rune "…": fpdf's core fonts (Courier included) encode text as
// cp1252/latin1, not UTF-8, so a multi-byte UTF-8 rune gets misread as several garbage cp1252 bytes.
func (r *pdfReport) fitCell(s string, maxW float64) string {
	if maxW <= 0 || r.pdf.GetStringWidth(s) <= maxW {
		return s
	}
	const ellipsis = "..."
	ellipsisW := r.pdf.GetStringWidth(ellipsis)
	if ellipsisW > maxW {
		return ""
	}
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		if r.pdf.GetStringWidth(string(runes))+ellipsisW <= maxW {
			return string(runes) + ellipsis
		}
	}
	return ellipsis
}

// Output renders the finished document. fpdf accumulates errors on the *Fpdf itself rather than
// returning them from each call (Cell, Table, ...), so this is the one place they surface -- a
// mismatched row width or similar layout mistake fails the export instead of shipping a broken file.
func (r *pdfReport) Output(w io.Writer) error {
	if err := r.pdf.Error(); err != nil {
		return err
	}
	return r.pdf.Output(w)
}
