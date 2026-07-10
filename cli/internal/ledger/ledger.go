// Package ledger renders entries as plain-text double-entry postings.
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

// WriteAll renders one entry per transaction, in order.
func WriteAll(w io.Writer, txs []model.Transaction, entries []model.Entry, currency string) error {
	if len(txs) != len(entries) {
		return fmt.Errorf("%d transactions but %d entries", len(txs), len(entries))
	}

	for i, tx := range txs {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writeEntry(w, tx, entries[i], currency); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(w io.Writer, tx model.Transaction, e model.Entry, currency string) error {
	// The books are the artifact. An entry whose postings do not account for the whole statement
	// line would be silently wrong once written, so it never gets written.
	if !e.Balances(tx) {
		return fmt.Errorf("%s %s: postings do not account for %s",
			tx.Date.Format("2006/01/02"), e.Payee, amount(-tx.AmountCents))
	}

	flag := "*"
	if e.Pending {
		flag = "!"
	}

	if _, err := fmt.Fprintf(w, "%s  %s %s\n", tx.Date.Format("2006/01/02"), flag, e.Payee); err != nil {
		return err
	}
	if e.Pending && e.Reason != "" {
		if _, err := fmt.Fprintf(w, "  ; needs review: %s\n", e.Reason); err != nil {
			return err
		}
	}
	for _, p := range e.Postings {
		if _, err := fmt.Fprintf(w, "  %s  %s %s\n", p.Account, amount(p.AmountCents), currency); err != nil {
			return err
		}
	}

	// The transaction already knows which account its statement came from, so that posting is
	// elided and its amount inferred. Nothing else can name it, and nothing else can unbalance it.
	_, err := fmt.Fprintf(w, "  %s\n", tx.Account)
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
