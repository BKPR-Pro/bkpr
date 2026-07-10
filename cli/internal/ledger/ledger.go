// Package ledger renders categorized transactions as plain-text double-entry entries.
//
// The output is the artifact: greppable, diffable, and readable by ledger-cli. Entries drawn from
// real bank data are cleared (*). A line that is a defensible default rather than a fact, or whose
// kind could not be determined, is written as pending (!) with its reason as a comment. Every line
// posts, so the books stay complete and balanced; `ledger print --uncleared` is the correction
// list.
package ledger

import (
	"fmt"
	"io"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

// SuspenseAccount holds a line whose kind is unknown. Never guess across kinds: an expense booked
// as income breaks the books and does not self-correct. A top-level suspense account also keeps
// the Income and Expenses totals honest while a line is unresolved, and `ledger bal Suspense`
// lists everything still unclassified.
const SuspenseAccount = "Suspense"

// WriteAll renders one entry per transaction, in order.
func WriteAll(w io.Writer, txs []model.Transaction, decisions []model.Decision, currency string) error {
	if len(txs) != len(decisions) {
		return fmt.Errorf("%d transactions but %d decisions", len(txs), len(decisions))
	}

	for i, tx := range txs {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writeEntry(w, tx, decisions[i], currency); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(w io.Writer, tx model.Transaction, d model.Decision, currency string) error {
	flag := "*"
	if d.NeedsReview {
		flag = "!"
	}

	payee := d.Payee
	if payee == "" {
		payee = tx.Description
	}

	category := d.Category
	if category == "" {
		category = SuspenseAccount
	}

	balance := d.Balance
	if balance == "" {
		balance = tx.Account
	}

	if _, err := fmt.Fprintf(w, "%s  %s %s\n", tx.Date.Format("2006/01/02"), flag, payee); err != nil {
		return err
	}
	if d.NeedsReview && d.Reason != "" {
		if _, err := fmt.Fprintf(w, "  ; needs review: %s\n", d.Reason); err != nil {
			return err
		}
	}

	// The statement's sign is from the source account's point of view, so the categorized posting
	// takes the opposite sign. The source account is elided and inferred by the ledger.
	if _, err := fmt.Fprintf(w, "  %s  %s %s\n", category, amount(-tx.AmountCents), currency); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  %s\n", balance)
	return err
}

func amount(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
