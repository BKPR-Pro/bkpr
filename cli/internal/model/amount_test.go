package model_test

import (
	"encoding/json"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

func TestParseAndRenderRoundTrip(t *testing.T) {
	cases := []struct {
		in    string
		units int64
		scale uint8
	}{
		{"84.20 CAD", 8420, 2},
		{"-40.00 CAD", -4000, 2},
		{"1500.00 USD", 150000, 2},
		{"10 AAPL", 10, 0}, // shares are not money; no decimal, no /100
		{"0.5 AAPL", 5, 1}, // and they can be fractional
		{"1234.56 CAD", 123456, 2},
	}
	for _, c := range cases {
		got, err := model.ParseAmount(c.in)
		if err != nil {
			t.Errorf("ParseAmount(%q): %v", c.in, err)
			continue
		}
		if got.Units != c.units || got.Scale != c.scale {
			t.Errorf("ParseAmount(%q) = {%d, %d}, want {%d, %d}", c.in, got.Units, got.Scale, c.units, c.scale)
		}
		if got.String() != c.in {
			t.Errorf("round trip of %q rendered %q", c.in, got.String())
		}
	}
}

func TestNewAmountTakesQuantityAndCommoditySeparately(t *testing.T) {
	// The CSV reader has the number and the account's commodity in hand separately.
	got, err := model.NewAmount("84.20", "CAD")
	if err != nil {
		t.Fatalf("NewAmount: %v", err)
	}
	if got.Units != 8420 || got.Scale != 2 || got.Commodity != "CAD" {
		t.Errorf("got %+v", got)
	}
}

func TestParseAcceptsAccountingShapes(t *testing.T) {
	// A leading + and a bare integer both parse; the point is only that no float is involved.
	if got, _ := model.NewAmount("+12", "CAD"); got.Units != 12 || got.Scale != 0 {
		t.Errorf("+12 = %+v", got)
	}
	if got, _ := model.NewAmount(".5", "CAD"); got.Units != 5 || got.Scale != 1 {
		t.Errorf(".5 = %+v", got)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "abc", "1.2.3", "12x", "CAD", "$5"} {
		if _, err := model.NewAmount(s, "CAD"); err == nil {
			t.Errorf("NewAmount(%q) should have failed", s)
		}
	}
	if _, err := model.NewAmount("5", ""); err == nil {
		t.Error("an amount with no commodity should fail")
	}
}

func TestNegateAndZero(t *testing.T) {
	a, _ := model.ParseAmount("84.20 CAD")
	if n := a.Negate(); n.Units != -8420 || n.String() != "-84.20 CAD" {
		t.Errorf("negate = %+v", n)
	}
	if z, _ := model.ParseAmount("0.00 CAD"); !z.IsZero() {
		t.Error("0.00 should be zero")
	}
}

func TestAddRequiresTheSameCommodity(t *testing.T) {
	cad, _ := model.ParseAmount("40.00 CAD")
	usd, _ := model.ParseAmount("40.00 USD")
	if _, err := cad.Add(usd); err == nil {
		t.Fatal("added two commodities; that needs a price")
	}
}

func TestAddSumsMinorUnits(t *testing.T) {
	a, _ := model.ParseAmount("40.00 CAD")
	b, _ := model.ParseAmount("44.20 CAD")
	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sum.String() != "84.20 CAD" {
		t.Errorf("sum = %q", sum.String())
	}
}

// Two spellings of the same value compare and add as equal, whatever their scale.
func TestEqualNormalizesScale(t *testing.T) {
	a, _ := model.ParseAmount("84.20 CAD")
	b, _ := model.ParseAmount("84.2 CAD")
	if !a.Equal(b) {
		t.Error("84.20 and 84.2 should be equal")
	}
}

// The log is committed and greppable, so an amount serializes as its ledger-style string, not as
// an object of fields.
func TestJSONIsALedgerStyleString(t *testing.T) {
	a, _ := model.ParseAmount("84.20 CAD")
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"84.20 CAD"` {
		t.Errorf("marshalled to %s, want the string form", b)
	}

	var back model.Amount
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != a {
		t.Errorf("round trip changed %+v to %+v", a, back)
	}
}
