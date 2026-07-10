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
//
// NeedsReview flags a line; it never blocks one. Every line posts. A flagged line is either a
// defensible default (an ambiguous merchant, where the attribution is real-world context the
// description does not contain) or a line whose kind is unknown. Both are written as pending so
// they are trivial to find and correct later. Guessing beats gating: a wrong leaf costs insight,
// and a gate costs the thing this tool exists to avoid.
type Decision struct {
	Payee       string
	Category    string
	Balance     string
	NeedsReview bool
	Reason      string // why the line was flagged, when it was
}

// Categorized reports whether a category was assigned. A flagged line is still categorized; it is
// just not confidently so.
func (d Decision) Categorized() bool {
	return d.Category != ""
}
