package main

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/BKPR-Pro/bkpr/lib/model"
)

// ANSI SGR color codes. Table output paints cells with these when writing to a terminal; a pipe or a
// file gets plain text, so the machine-readable forms are byte-identical whether or not a person is
// watching.
const (
	ansiBold   = "1"
	ansiRed    = "31"
	ansiGreen  = "32"
	ansiYellow = "33"
	ansiBlue   = "34"
	ansiGray   = "90"
)

// tablePalette paints a cell, or returns it untouched when color is off. The visible text is wrapped
// in an SGR color and a reset; padding is added by the caller outside this, so column widths are
// measured on the bare text and a colored table lands identically to its plain form.
type tablePalette struct{ on bool }

func (p tablePalette) paint(code, s string) string {
	if !p.on || code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// colorMode resolves the -color flag against the environment: never off, always on, auto on only at a
// terminal and only when NO_COLOR is unset (the de-facto standard for opting out).
func colorMode(mode string, tty, noColor bool) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default:
		return tty && !noColor
	}
}

// colorFor picks a cell's color by meaning: money in is green and money out red, an unplaced line is
// amber wherever it appears, and the ledgers of accounts read blue.
func amountColor(a model.Amount) string {
	if a.Units < 0 {
		return ansiRed
	}
	return ansiGreen
}

func postsColor(account string) string {
	if strings.Contains(account, model.Uncategorized) {
		return ansiYellow
	}
	return ansiBlue
}

// noColorSet reports whether NO_COLOR is present and non-empty in the environment.
func noColorSet() bool { return os.Getenv("NO_COLOR") != "" }

// cell is one table cell: the text a person reads and the color it carries (empty for none). Color is
// applied only when the palette is on, and always outside the padding, so widths are measured on the
// visible text alone and a colored table lands identically to its plain form.
type cell struct {
	text string
	code string
}

// grid lays out a fixed-column table by hand rather than through text/tabwriter, whose escape handling
// miscounts ANSI cells. Columns are padded to the widest visible cell plus a two-space gutter; the
// last column is left ragged, exactly as the plain table always was.
type grid struct {
	header []cell
	rows   [][]cell
}

func (g grid) write(out io.Writer, p tablePalette) {
	n := len(g.header)
	width := make([]int, n)
	measure := func(row []cell) {
		for j := 0; j < n; j++ {
			if w := utf8.RuneCountInString(row[j].text); w > width[j] {
				width[j] = w
			}
		}
	}
	measure(g.header)
	for _, r := range g.rows {
		measure(r)
	}
	writeRow := func(row []cell) {
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteString(p.paint(row[j].code, row[j].text))
			if j < n-1 {
				b.WriteString(strings.Repeat(" ", width[j]-utf8.RuneCountInString(row[j].text)+2))
			}
		}
		b.WriteByte('\n')
		io.WriteString(out, b.String())
	}
	writeRow(g.header)
	for _, r := range g.rows {
		writeRow(r)
	}
}
