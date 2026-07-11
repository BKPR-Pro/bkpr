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

	// Source names the live connector this came from (e.g. "rent"), empty for a file import. It is
	// provenance the fold sets from the import's actor, and it is what tells a pulled rent payment
	// apart from the bank deposit that is the same money.
	Source string
}

// Posting is one side of an entry: an account, and a signed amount.
type Posting struct {
	Account string `json:"account"`
	Amount  Amount `json:"amount"`
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
}

// Balances reports whether the postings account for the whole statement line. The statement's sign
// is from the source account's point of view, so the categorized side takes the opposite one. A
// posting in another commodity cannot be summed without a price, so a mixed-commodity entry never
// balances; that boundary holds until prices are built.
func (e Entry) Balances(tx Transaction) bool {
	sum := Amount{Commodity: tx.Amount.Commodity}
	for _, p := range e.Postings {
		next, err := sum.Add(p.Amount)
		if err != nil {
			return false
		}
		sum = next
	}
	return sum.Equal(tx.Amount.Negate())
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
