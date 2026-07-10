// Package model holds the normalized shapes the core understands. Connectors translate the
// outside world into these; nothing inside the core knows about CSV columns, bank APIs, or
// any particular accounting app.
package model

import "time"

// Transaction is one normalized bank or card line. Every Source connector produces these,
// whatever its transport. ID is a stable fingerprint of the line and is the idempotency root:
// re-importing the same statement produces the same IDs, so nothing is ever recorded twice.
type Transaction struct {
	ID          string            // stable fingerprint of this line
	Account     string            // ledger account the statement belongs to, e.g. Assets:Bank:Chequing
	Date        time.Time         // when the line posted
	AmountCents int64             // signed; negative is money out
	Description string            // the raw memo the bank gave us
	Raw         map[string]string // the original columns, kept for auditing
}

// Decision is what categorization concluded about one Transaction.
//
// Category is the income or expense account the money belongs to. Balance is the account that
// balances the entry (in ledger terms the elided posting, whose amount is omitted and inferred).
// A Decision with NeedsReview set is one the machine would not guess at: it goes to the human,
// and the answer becomes a rule so the same question is never asked twice.
type Decision struct {
	Payee       string
	Category    string
	Balance     string
	NeedsReview bool
	Reason      string // why review is needed, when it is
}

// Categorized reports whether the decision is complete enough to post.
func (d Decision) Categorized() bool {
	return !d.NeedsReview && d.Category != "" && d.Balance != ""
}
