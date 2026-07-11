package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Amount is an exact quantity of one commodity.
//
// It is held as integer minor units so no float ever touches the books: Units is the quantity in
// those units, and Scale is how many decimal places make one whole. CAD is scale 2 (cents), a
// zero-decimal currency is scale 0, and a share is whatever the statement showed. Commodity is the
// symbol, which need not be money: "CAD", "USD", "AAPL". This is ledger-cli's model of an amount,
// which is why the books it emits can hold anything ledger can.
//
// An entry may mix commodities: buying a stock for cash holds shares and dollars in one entry. They
// balance through a price on the posting (see Posting.Cost), which resolves the foreign commodity
// back to the line's own. An amount itself is still one commodity; the price lives a level up.
type Amount struct {
	Units     int64
	Scale     uint8
	Commodity string
}

// ParseAmount reads the ledger-style "<quantity> <commodity>" form, e.g. "84.20 CAD" or "10 AAPL".
func ParseAmount(s string) (Amount, error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return Amount{}, fmt.Errorf("amount %q must be %q", s, "<quantity> <commodity>")
	}
	return NewAmount(parts[0], parts[1])
}

// ParsePosting reads a -post value: a quantity, with an optional commodity, and an optional total
// price introduced by "@@".
//
//	"40.00"                   40.00 in the fallback commodity, no price
//	"10 AAPL"                 10 AAPL, no price (a share posting still needs one to balance a line)
//	"10 AAPL @@ 1000.00 USD"  10 AAPL priced at a 1000.00 USD total
//
// A per-unit "@" is refused: dividing a total by a quantity reintroduces the rounding that a total
// avoids, and the cost basis is meant to be the exact cash paid. The returned cost is a magnitude;
// the quantity's sign is what a posting reads to know a buy from a sell.
func ParsePosting(s, fallbackCommodity string) (Amount, *Amount, error) {
	quantity, price, priced := strings.Cut(s, "@@")
	if strings.Contains(quantity, "@") {
		return Amount{}, nil, fmt.Errorf("price %q must be a total with %q, not a per-unit %q", s, "@@", "@")
	}

	amount, err := parseQuantity(quantity, fallbackCommodity)
	if err != nil {
		return Amount{}, nil, err
	}
	if !priced {
		return amount, nil, nil
	}

	cost, err := ParseAmount(strings.TrimSpace(price))
	if err != nil {
		return Amount{}, nil, err
	}
	if cost.Units < 0 {
		cost = cost.Negate()
	}
	return amount, &cost, nil
}

// parseQuantity reads the quantity half of a posting: "10 AAPL" carries its own commodity, while a
// bare "40.00" takes the fallback the caller supplies (the line's own commodity).
func parseQuantity(s, fallbackCommodity string) (Amount, error) {
	s = strings.TrimSpace(s)
	if len(strings.Fields(s)) >= 2 {
		return ParseAmount(s)
	}
	return NewAmount(s, fallbackCommodity)
}

// NewAmount builds an amount from a quantity and a commodity given separately, which is what the
// CSV reader and the CLI have in hand.
func NewAmount(quantity, commodity string) (Amount, error) {
	if commodity == "" {
		return Amount{}, fmt.Errorf("amount %q has no commodity", quantity)
	}
	units, scale, err := parseDecimal(quantity)
	if err != nil {
		return Amount{}, err
	}
	return Amount{Units: units, Scale: scale, Commodity: commodity}, nil
}

// parseDecimal reads a plain decimal into integer minor units and a scale, without a float. The
// digits on either side of the point are one integer; the scale is how many followed the point.
func parseDecimal(s string) (int64, uint8, error) {
	s = strings.TrimSpace(s)
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}

	intPart, fracPart, _ := strings.Cut(s, ".")
	digits := intPart + fracPart
	if digits == "" {
		return 0, 0, fmt.Errorf("amount %q is not a number", s)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, 0, fmt.Errorf("amount %q is not a number", s)
		}
	}

	units, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("amount %q is out of range", s)
	}
	if neg {
		units = -units
	}
	return units, uint8(len(fracPart)), nil
}

// String renders the canonical ledger form.
func (a Amount) String() string {
	if a.Scale == 0 {
		return fmt.Sprintf("%d %s", a.Units, a.Commodity)
	}
	u := a.Units
	sign := ""
	if u < 0 {
		sign, u = "-", -u
	}
	div := pow10(a.Scale)
	return fmt.Sprintf("%s%d.%0*d %s", sign, u/div, int(a.Scale), u%div, a.Commodity)
}

// Negate flips the sign, keeping scale and commodity.
func (a Amount) Negate() Amount {
	return Amount{Units: -a.Units, Scale: a.Scale, Commodity: a.Commodity}
}

// IsZero reports whether the quantity is zero.
func (a Amount) IsZero() bool { return a.Units == 0 }

// Add sums two amounts of the same commodity. Different commodities cannot be added directly; a
// price resolves one to the other first (see Posting.value), so at this level a mismatch is an
// error rather than a guess.
func (a Amount) Add(b Amount) (Amount, error) {
	if a.Commodity != b.Commodity {
		return Amount{}, fmt.Errorf("cannot add %s and %s in one entry without a price", a.Commodity, b.Commodity)
	}
	s := a.Scale
	if b.Scale > s {
		s = b.Scale
	}
	return Amount{Units: a.at(s) + b.at(s), Scale: s, Commodity: a.Commodity}, nil
}

// Equal reports whether two amounts are the same commodity and the same value, regardless of how
// many decimal places each was written with.
func (a Amount) Equal(b Amount) bool {
	if a.Commodity != b.Commodity {
		return false
	}
	s := a.Scale
	if b.Scale > s {
		s = b.Scale
	}
	return a.at(s) == b.at(s)
}

// at returns the units restated at a finer scale.
func (a Amount) at(scale uint8) int64 { return a.Units * pow10(scale-a.Scale) }

// MarshalJSON writes the ledger-style string, so the committed log reads like a ledger and its
// diffs are legible.
func (a Amount) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

func (a *Amount) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseAmount(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func pow10(n uint8) int64 {
	r := int64(1)
	for i := uint8(0); i < n; i++ {
		r *= 10
	}
	return r
}
