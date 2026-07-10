// Package model holds the normalized shapes the core understands. Connectors translate the
// outside world into these; nothing inside the core knows about CSV columns, bank APIs, or
// any particular accounting app.
package model

import "time"

// SuspenseAccount holds a line whose kind is unknown. Never guess across kinds: an expense booked
// as income breaks the books and does not self-correct, while a wrong leaf costs only insight. A
// top-level suspense account keeps the Income and Expenses totals honest while a line is
// unresolved, and `ledger bal Suspense` lists everything still unclassified.
const SuspenseAccount = "Suspense"

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

// Posting is one side of an entry: an account, and a signed amount in cents.
type Posting struct {
	Account     string `json:"account"`
	AmountCents int64  `json:"amount_cents"`
}

// Entry is what one Transaction becomes in the books.
//
// Postings are the categorized side only. The posting against the transaction's own account is
// elided and inferred by the ledger, exactly as ledger-cli does, which is why an entry cannot be
// unbalanced: the categorized postings must account for the whole line. A split is simply more
// than one of them, and it is the shape a real correction usually takes, because one charge can
// serve two properties.
//
// Pending flags an entry; it never withholds one. Every line posts. A flagged entry is either a
// defensible default (an ambiguous merchant, where the attribution is real-world context the
// description does not contain) or a line whose kind is unknown and so parked in Suspense. Both
// are written as pending so they are trivial to find and correct later.
type Entry struct {
	Payee    string
	Postings []Posting
	Pending  bool
	Reason   string // why the entry was flagged, when it was
}

// Balances reports whether the postings account for the whole statement line. The statement's sign
// is from the source account's point of view, so the categorized side takes the opposite one.
func (e Entry) Balances(tx Transaction) bool {
	var sum int64
	for _, p := range e.Postings {
		sum += p.AmountCents
	}
	return sum == -tx.AmountCents
}
