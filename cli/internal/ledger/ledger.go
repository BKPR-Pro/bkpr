// Package ledger renders categorized transactions as plain-text double-entry entries.
//
// The output is the artifact: greppable, diffable, and readable by ledger-cli. Entries drawn from
// real bank data are cleared (*). A line the machine could not categorize is written as pending
// (!) against a placeholder account, with the reason as a comment, so the books stay complete and
// the open questions are trivial to find. Nothing is ever guessed at.
package ledger

import (
	"fmt"
	"io"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

// UnknownAccount holds the amount for a line awaiting review, so the entry still balances.
const UnknownAccount = "Expenses:Unknown"

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
		category = UnknownAccount
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
