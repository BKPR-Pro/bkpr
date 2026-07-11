// Package ledger renders entries as plain-text double-entry postings.
//
// The output is the artifact: greppable, diffable, and readable by ledger-cli. Every entry is
// cleared (*), because every line came off a bank statement and so has cleared the bank. Pending
// (!) means the bank has not reported a transaction yet, and nothing written here is that.
//
// Nothing else is annotated. Where the rules ran out of knowledge the account path says so, and
// `ledger bal Uncategorized` finds every one of them at any depth.
package ledger

import (
	"fmt"
	"io"

	"github.com/dallasread/bookkeeper/lib/model"
)

// WriteAll renders one entry per transaction, in order. Each entry is written in the currency of
// the account its statement came from, which is a fact about the account rather than a choice made
// at render time.
func WriteAll(w io.Writer, txs []model.Transaction, entries []model.Entry) error {
	if len(txs) != len(entries) {
		return fmt.Errorf("%d transactions but %d entries", len(txs), len(entries))
	}

	for i, tx := range txs {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writeEntry(w, tx, entries[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(w io.Writer, tx model.Transaction, e model.Entry) error {
	if tx.Amount.Commodity == "" {
		return fmt.Errorf("%s %s: the transaction has no commodity", tx.Date.Format("2006/01/02"), e.Payee)
	}

	// The books are the artifact. An entry whose postings do not account for the whole statement
	// line would be silently wrong once written, so it never gets written.
	if !e.Balances(tx) {
		return fmt.Errorf("%s %s: postings do not account for %s",
			tx.Date.Format("2006/01/02"), e.Payee, tx.Amount.Negate())
	}

	if _, err := fmt.Fprintf(w, "%s  * %s\n", tx.Date.Format("2006/01/02"), e.Payee); err != nil {
		return err
	}
	for _, p := range e.Postings {
		// A posting in another commodity (shares bought with cash) carries its total price in the
		// "@@" form, which is what lets ledger value the position and match cost basis on a sale.
		if p.Cost != nil {
			cost := *p.Cost
			if cost.Units < 0 {
				cost = cost.Negate()
			}
			if _, err := fmt.Fprintf(w, "  %s  %s @@ %s\n", p.Account, p.Amount, cost); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "  %s  %s\n", p.Account, p.Amount); err != nil {
			return err
		}
	}

	// The transaction already knows which account its statement came from, so that posting is
	// elided and its amount inferred. Nothing else can name it, and nothing else can unbalance it.
	_, err := fmt.Fprintf(w, "  %s\n", tx.Account)
	return err
}
