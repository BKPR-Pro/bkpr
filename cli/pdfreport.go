package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/go-pdf/fpdf"
)

// pdfReport is the shared layout every PDF export (invoice, register, accounts due) builds on: a
// letterhead-style title, plain text lines, and a monospace-ish table with a header row -- the same
// three primitives the text and HTML renderers already lean on, just aimed at a page instead of a
// terminal or a browser.
type pdfReport struct {
	pdf *fpdf.Fpdf
	// winAnsi maps UTF-8 to the one-byte encoding the core fonts draw; fpdf writes a string's bytes as given.
	winAnsi func(string) string
	// bare drops the page footer from a document that fits one page.
	bare, closing bool
}

// newPdfReport starts a single-page-growing A4 document with page numbers in the footer, margins
// tight enough to fit a register's wide rows.
func newPdfReport() *pdfReport {
	pdf := fpdf.New("P", "mm", "A4", "")
	r := &pdfReport{pdf: pdf, winAnsi: pdf.UnicodeTranslatorFromDescriptor("")}
	pdf.SetMargins(12, 12, 12)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		if r.bare && r.closing && pdf.PageNo() == 1 {
			return
		}
		pdf.SetY(-15)
		pdf.SetFont("Courier", "", 8)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetFont("Courier", "", 10)
	return r
}

// encode turns s into what the core fonts can draw: a rune that shows nothing (U+200E, a zero-width
// space) is dropped, so a line holding only one is a blank line, and the rest map to WinAnsi, with a
// "." for a character it lacks.
func (r *pdfReport) encode(s string) string {
	return r.winAnsi(strings.Map(func(c rune) rune {
		if !unicode.IsGraphic(c) {
			return -1
		}
		return c
	}, s))
}

// Title sets the document's banner line, bold and a couple of sizes up -- the letterhead a printed
// statement or report opens with.
func (r *pdfReport) Title(s string) {
	r.pdf.SetFont("Courier", "B", 16)
	r.pdf.CellFormat(0, 10, r.encode(s), "", 1, "L", false, 0, "")
	r.pdf.SetFont("Courier", "", 10)
	r.pdf.Ln(2)
}

// Line writes one line of plain body text, e.g. a reference, a date, or a totals line.
func (r *pdfReport) Line(s string) {
	r.Row(s, "", "")
}

// Row writes one body line with left at the left margin and right at the right margin, either of
// which may be empty; style is fpdf's, "" for regular and "B" for bold. A left too wide for the room
// right leaves it is truncated the way a table cell is.
func (r *pdfReport) Row(left, right, style string) {
	left, right = r.encode(left), r.encode(right)
	r.pdf.SetFont("Courier", style, 10)
	pageW, _, _ := r.pdf.PageSize(0)
	ml, _, mr, _ := r.pdf.GetMargins()
	rightW := 0.0
	if right != "" {
		rightW = r.pdf.GetStringWidth(right) + cellPad
	}
	leftW := pageW - ml - mr - rightW
	r.pdf.CellFormat(leftW, 6, r.fitCell(left, leftW-cellPad), "", 0, "L", false, 0, "")
	r.pdf.CellFormat(rightW, 6, right, "", 1, "R", false, 0, "")
	r.pdf.SetFont("Courier", "", 10)
}

// Rule draws a hairline across the page with a little air around it, e.g. above a total.
func (r *pdfReport) Rule() {
	r.pdf.Ln(3)
	r.pdf.CellFormat(0, 3, "", "T", 1, "", false, 0, "")
}

const cellPad = 2 // mm of left+right padding fpdf's CellFormat leaves inside a cell

// Table writes a header row and its data rows. Columns split the printable width proportionally to
// the given weights (one per header; a missing or non-positive weight falls back to 1, i.e. an equal
// share) -- callers with a column that reliably needs more room, e.g. a register's Fingerprint and
// Account, hand in a larger weight for it. Whatever still does not fit its
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

	r.pdf.SetFont("Courier", "B", 8)
	for i, h := range headers {
		r.pdf.CellFormat(colW[i], 7, r.fitCell(r.encode(h), colW[i]-cellPad), "B", 0, "L", false, 0, "")
	}
	r.pdf.Ln(-1)

	r.pdf.SetFont("Courier", "", 8)
	for _, row := range rows {
		if len(row) != len(headers) {
			r.pdf.SetErrorf("pdf table row has %d cell(s), want %d to match the header", len(row), len(headers))
			return
		}
		for i, cell := range row {
			r.pdf.CellFormat(colW[i], 6, r.fitCell(r.encode(cell), colW[i]-cellPad), "", 0, "L", false, 0, "")
		}
		r.pdf.Ln(-1)
	}
	r.pdf.Ln(3)
}

// fitCell truncates s with a trailing ellipsis, character by character, until its rendered width
// (in the current font) fits within maxW -- so a cell too wide for its column shrinks instead of
// spilling into the next one. s is returned unchanged when it already fits. The ellipsis is plain
// ASCII ("...") because s is already WinAnsi bytes (see encode), not UTF-8.
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
	r.closing = true
	return r.pdf.Output(w)
}
