package source_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/source"
)

func signedMapping() source.CSV {
	return source.CSV{
		Account:     "Assets:Bank:Chequing",
		Date:        "Date",
		Description: "Description",
		Amount:      "Amount",
		DateFormat:  "2006-01-02",
	}
}

func TestReadsSignedAmountCSV(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Amount\n" +
			"2026-03-01,ACME HARDWARE #4471,-84.20\n" +
			"2026-03-02,RENT E-TRANSFER FROM J SMITH,1600.00\n")

	txs, err := source.ReadCSV(in, signedMapping())
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}

	first := txs[0]
	if first.Account != "Assets:Bank:Chequing" {
		t.Errorf("account = %q", first.Account)
	}
	if got := first.Date.Format("2006-01-02"); got != "2026-03-01" {
		t.Errorf("date = %q", got)
	}
	if first.AmountCents != -8420 {
		t.Errorf("cents = %d, want -8420", first.AmountCents)
	}
	if first.Description != "ACME HARDWARE #4471" {
		t.Errorf("description = %q", first.Description)
	}
	if txs[1].AmountCents != 160000 {
		t.Errorf("cents = %d, want 160000", txs[1].AmountCents)
	}
	if first.Raw["Description"] == "" {
		t.Error("expected raw columns to be kept for auditing")
	}
}

// Banks format money inconsistently. Accept the common shapes rather than making the user
// pre-clean their export.
func TestParsesMessyAmounts(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Amount\n" +
			"2026-03-01,A,\"$1,234.56\"\n" +
			"2026-03-02,B,(45.00)\n" +
			"2026-03-03,C,+12\n")

	txs, err := source.ReadCSV(in, signedMapping())
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}

	want := []int64{123456, -4500, 1200}
	for i, w := range want {
		if txs[i].AmountCents != w {
			t.Errorf("row %d cents = %d, want %d", i, txs[i].AmountCents, w)
		}
	}
}

// Many banks export debits and credits as separate columns instead of one signed column.
func TestDebitCreditColumns(t *testing.T) {
	in := strings.NewReader(
		"Date,Description,Debit,Credit\n" +
			"2026-03-01,PURCHASE,84.20,\n" +
			"2026-03-02,DEPOSIT,,1600.00\n")

	m := source.CSV{
		Account: "Assets:Bank:Chequing", Date: "Date", Description: "Description",
		Debit: "Debit", Credit: "Credit", DateFormat: "2006-01-02",
	}

	txs, err := source.ReadCSV(in, m)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if txs[0].AmountCents != -8420 {
		t.Errorf("debit cents = %d, want -8420", txs[0].AmountCents)
	}
	if txs[1].AmountCents != 160000 {
		t.Errorf("credit cents = %d, want 160000", txs[1].AmountCents)
	}
}

// The ID is the idempotency root: re-importing a statement must produce the same IDs, or the
// hub would double-record. Two genuinely identical lines are a real thing (the same charge
// twice in a day) and must stay distinct.
func TestIDsAreStableAndDistinguishIdenticalLines(t *testing.T) {
	body := "Date,Description,Amount\n" +
		"2026-03-01,COFFEE HOUSE 12,-5.00\n" +
		"2026-03-01,COFFEE HOUSE 12,-5.00\n" +
		"2026-03-01,ACME HARDWARE,-5.00\n"

	first, err := source.ReadCSV(strings.NewReader(body), signedMapping())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ReadCSV(strings.NewReader(body), signedMapping())
	if err != nil {
		t.Fatal(err)
	}

	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("row %d: re-import changed the ID (%q vs %q)", i, first[i].ID, second[i].ID)
		}
		if first[i].ID == "" {
			t.Errorf("row %d: empty ID", i)
		}
	}
	if first[0].ID == first[1].ID {
		t.Error("two identical lines collapsed to one ID; the second charge would be lost")
	}
	if first[0].ID == first[2].ID {
		t.Error("different descriptions produced the same ID")
	}
}

func TestMissingColumnIsAnError(t *testing.T) {
	in := strings.NewReader("Date,Memo,Amount\n2026-03-01,X,-1.00\n")
	if _, err := source.ReadCSV(in, signedMapping()); err == nil {
		t.Fatal("expected an error naming the missing Description column")
	}
}

func TestBadDateIsAnError(t *testing.T) {
	in := strings.NewReader("Date,Description,Amount\nnot-a-date,X,-1.00\n")
	if _, err := source.ReadCSV(in, signedMapping()); err == nil {
		t.Fatal("expected an error for an unparseable date")
	}
}
