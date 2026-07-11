// Package model holds the normalized shapes the core understands. Connectors translate the
// outside world into these; nothing inside the core knows about CSV columns, bank APIs, or
// any particular accounting app.
package model

import (
	"strings"
	"time"
)

// Uncategorized is where an account path stops when the rules run out of knowledge. As a leaf it
// says the kind is known but the detail is not (Expenses:Real Estate:Materials:Uncategorized). As
// the whole account it says not even the kind is known (Uncategorized).
//
// It is not a guess and not a warning. It is the truth about what the rules can tell, so every
// total above it stays honest, and `ledger bal Uncategorized` finds all of them at once. The fix
// is usually a better rule, which reclassifies the whole history at once, rather than a
// correction on each line.
const Uncategorized = "Uncategorized"

// Transaction is one normalized bank or card line. Every Source connector produces these,
// whatever its transport. ID is a stable fingerprint of the line and is the idempotency root:
// re-importing the same statement produces the same IDs, so nothing is ever recorded twice.
type Transaction struct {
	ID          string            // stable fingerprint of this line
	Account     string            // ledger account the statement belongs to, e.g. Assets:Bank:Chequing
	Date        time.Time         // when the line posted
	Amount      Amount            // signed; negative is money out. Carries its own commodity.
	Description string            // the raw memo the bank gave us
	Raw         map[string]string // the original columns, kept for auditing
}

// Posting is one side of an entry: an account, and a signed amount.
//
// Cost is the optional total price of the posting in the statement's commodity, the ledger "@@"
// form. It is present only when the amount is in a different commodity than the line: buying 10
// AAPL with USD cash records `10 AAPL` with a Cost of the dollars it took. It is a total, never a
// per-unit price, so the basis stays an exact quantity of cash with no rounding. Nil for the
// ordinary same-commodity posting.
type Posting struct {
	Account string  `json:"account"`
	Amount  Amount  `json:"amount"`
	Cost    *Amount `json:"cost,omitempty"`
}

// value is what the posting contributes toward balancing the line, in the statement's commodity. A
// plain posting contributes its own amount. A priced posting contributes its total cost, signed to
// follow the quantity: shares acquired add the cash they cost, shares disposed subtract the cash
// they raised. The cost is carried as a magnitude, and the quantity's sign is what decides it.
func (p Posting) value() Amount {
	if p.Cost == nil {
		return p.Amount
	}
	c := *p.Cost
	if c.Units < 0 {
		c = c.Negate()
	}
	if p.Amount.Units < 0 {
		return c.Negate()
	}
	return c
}

// Entry is what one Transaction becomes in the books.
//
// Postings are the categorized side only. The posting against the transaction's own account is
// elided and inferred by the ledger, exactly as ledger-cli does, which is why an entry cannot be
// unbalanced: the categorized postings must account for the whole line. A split is simply more
// than one of them, and it is the shape a real correction usually takes, because one charge can
// serve two properties.
type Entry struct {
	Payee    string
	Postings []Posting

	// Metadata is an opaque bag carried from the rule that categorized the line. The core neither
	// reads nor validates it; a connector reads its own namespaced keys (e.g. rentapp.lease) to
	// learn where to export. Empty when no rule supplied any.
	Metadata map[string]string

	// Gain names the account that absorbs a sale's capital gain or loss, e.g. Income:Capital Gains.
	// It is set only on a disposal: the shares leaving are valued at their cost base, and whatever
	// is left over between that base and the proceeds is the realized gain. The base, and so the
	// gain, is computed by folding the account's history, never stored, so a corrected purchase
	// price reclassifies the gain on the next regeneration. Empty on an ordinary entry.
	Gain string
}

// Balances reports whether the postings account for the whole statement line. The statement's sign
// is from the source account's point of view, so the categorized side takes the opposite one. Each
// posting is summed at its value in the line's commodity: a same-commodity posting is its amount,
// and a posting in another commodity resolves through its price. A cross-commodity posting with no
// price, or a price in a third commodity, cannot be summed and so never balances.
func (e Entry) Balances(tx Transaction) bool {
	short, err := e.Shortfall(tx)
	return err == nil && short.IsZero()
}

// Shortfall reports what one more posting would need to contribute for the entry to account for the
// whole line: the line's negation, less what the postings already cover. It is zero when the entry
// balances, and on a sale it is exactly the amount the capital-gains posting takes, the difference
// between the proceeds and the cost base the shares left at. The error is the mixed-commodity one:
// a posting that cannot be summed against the line, because it carries no price or a foreign one.
func (e Entry) Shortfall(tx Transaction) (Amount, error) {
	sum := Amount{Commodity: tx.Amount.Commodity}
	for _, p := range e.Postings {
		next, err := sum.Add(p.value())
		if err != nil {
			return Amount{}, err
		}
		sum = next
	}
	return tx.Amount.Negate().Add(sum.Negate())
}

// Uncategorized reports whether any posting stops short of a full account path.
func (e Entry) Uncategorized() bool {
	for _, p := range e.Postings {
		if p.Account == Uncategorized || strings.HasSuffix(p.Account, ":"+Uncategorized) {
			return true
		}
	}
	return false
}
