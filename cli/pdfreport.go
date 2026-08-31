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

// Table writes a header row and its data rows, columns sized equally across the printable width --
// good enough for the fixed-column layouts (invoice items, a register, accounts due) this tool
// renders; nothing here claims to be a general table layout engine.
func (r *pdfReport) Table(headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	pageW, _, _ := r.pdf.PageSize(0)
	left, _, right, _ := r.pdf.GetMargins()
	colW := (pageW - left - right) / float64(len(headers))

	r.pdf.SetFont("Courier", "B", 9)
	for _, h := range headers {
		r.pdf.CellFormat(colW, 7, h, "B", 0, "L", false, 0, "")
	}
	r.pdf.Ln(-1)

	r.pdf.SetFont("Courier", "", 9)
	for _, row := range rows {
		if len(row) != len(headers) {
			r.pdf.SetErrorf("pdf table row has %d cell(s), want %d to match the header", len(row), len(headers))
			return
		}
		for _, cell := range row {
			r.pdf.CellFormat(colW, 6, cell, "", 0, "L", false, 0, "")
		}
		r.pdf.Ln(-1)
	}
	r.pdf.Ln(3)
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
