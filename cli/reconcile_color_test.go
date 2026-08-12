package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

func recDay(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func recCAD(units int64) model.Amount {
	return model.Amount{Units: units, Scale: 2, Commodity: "CAD"}
}

// An account that agrees with the bank paints its delta and the all-clear verdict green, and stripping
// the color reproduces the plain report exactly.
func TestReconcileColorsAMatchGreenAndStripsToPlain(t *testing.T) {
	recs := []books.Reconciliation{{
		Account: "Assets:Bank:Chequing", AsOf: recDay("2026-03-12"), Since: recDay("2026-03-12"),
		Bank: recCAD(363068), Books: recCAD(363068), Delta: recCAD(0), Reconciled: true, Anchored: true,
	}}

	var plain, colored bytes.Buffer
	writeReconcile(&plain, recs, false)
	writeReconcile(&colored, recs, true)

	if strings.Contains(plain.String(), "\x1b[") {
		t.Error("plain reconcile must carry no ANSI codes")
	}
	if !strings.Contains(colored.String(), "\x1b[32m") {
		t.Error("a matched account and its verdict should be green")
	}
	if !strings.Contains(colored.String(), "all accounts reconcile to the penny") {
		t.Error("a clean book should print the all-clear verdict")
	}
	if ansiStrip(colored.String()) != plain.String() {
		t.Errorf("stripping color must reproduce the plain reconcile exactly\nplain:\n%s\nstripped:\n%s", plain.String(), ansiStrip(colored.String()))
	}
}

// A real gap paints its delta red and withholds the all-clear.
func TestReconcileColorsAGapRed(t *testing.T) {
	recs := []books.Reconciliation{{
		Account: "Assets:Bank:Chequing", AsOf: recDay("2026-03-12"), Since: recDay("2026-02-01"),
		Bank: recCAD(363068), Books: recCAD(362568), Delta: recCAD(-500), Reconciled: false,
	}}

	var colored bytes.Buffer
	writeReconcile(&colored, recs, true)

	if !strings.Contains(colored.String(), "\x1b[31m") {
		t.Error("a nonzero delta should be red")
	}
	if strings.Contains(colored.String(), "reconcile to the penny") {
		t.Error("a book with a gap must not claim it reconciles")
	}
}
