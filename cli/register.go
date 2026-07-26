package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/store"
)

// registerCmd reads the books the way the bank prints a statement: every line in date order with
// the door it entered through, or one account's statement with a running balance. It reads through
// the same lens books does -- the same -basis and -since fold whose lines make books' totals -- so
// the two commands can never show different books. -dups reads that fold for candidate twins -- one
// purchase entering through two doors -- which fingerprints cannot dedupe and reconcile cannot see.
// It is a pure reading: nothing is written, ever.
func registerCmd(args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	account := fs.String("account", "", "show one account's statement, with a running balance; a pattern, matched anywhere in the path")
	basis := fs.String("basis", "cash", "accounting basis: cash or accrual")
	since := fs.String("since", "", "on -basis accrual, book only invoices/bills dated on or after this (YYYY-MM-DD)")
	from := fs.String("from", "", "show only lines dated on or after this (YYYY-MM-DD)")
	to := fs.String("to", "", "show only lines dated on or before this (YYYY-MM-DD)")
	dups := fs.Bool("dups", false, "report candidate twins: lines sharing a date and amount that entered through different doors")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basis != string(books.CashBasis) && *basis != string(books.AccrualBasis) {
		return fmt.Errorf("unknown basis %q: want cash or accrual", *basis)
	}
	var effective time.Time
	if *since != "" {
		if *basis != string(books.AccrualBasis) {
			return fmt.Errorf("-since only applies to -basis accrual")
		}
		var err error
		if effective, err = time.Parse("2006-01-02", *since); err != nil {
			return fmt.Errorf("-since %q is not YYYY-MM-DD", *since)
		}
	}
	fromDay, toDay, err := periodBounds(*from, *to)
	if err != nil {
		return err
	}

	s, err := store.OpenReader(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := books.LedgerBasisSince(s.Log, books.Basis(*basis), effective)
	if err != nil {
		return err
	}
	doors, err := books.Doors(s.Log)
	if err != nil {
		return err
	}
	if !fromDay.IsZero() || !toDay.IsZero() {
		txs, entries = filterByDate(fromDay, toDay, txs, entries)
	}

	if *dups {
		// -account narrows the hunt the same way it narrows books: to the lines posting there.
		if *account != "" {
			if txs, entries, err = filterByAccount([]string{*account}, txs, entries); err != nil {
				return err
			}
		}
		settlements, err := books.Settlements(s.Log)
		if err != nil {
			return err
		}
		return renderTwins(os.Stdout, twinGroups(txs, entries, doors, settlements))
	}

	var re *regexp.Regexp
	if *account != "" {
		if re, err = regexp.Compile("(?i)" + *account); err != nil {
			return fmt.Errorf("-account %q is not a valid pattern: %v", *account, err)
		}
	}
	rows, err := buildRegister(txs, entries, doors, re)
	if err != nil {
		return err
	}
	return renderRegister(os.Stdout, rows, re != nil)
}

// registerRow is one statement line as the register prints it: the movement, the account it moved,
// and the door it entered through. Balance runs only when the register is filtered to an account,
// because a balance across different accounts is not a number.
type registerRow struct {
	ID      string
	Date    time.Time
	Payee   string
	Amount  model.Amount
	Balance model.Amount
	Account string
	Door    string
}

// buildRegister folds the reading into statement rows. With no pattern it is every account's
// statement interleaved, one row per line. With a pattern it is the matched account's own
// statement: each row the net movement the line made on the matched accounts -- the source leg the
// fold books there or a posting that landed there -- with a balance running per commodity. A line
// whose matched legs net to zero moved nothing and is left out.
func buildRegister(txs []model.Transaction, entries []model.Entry, doors map[string]string, re *regexp.Regexp) ([]registerRow, error) {
	var rows []registerRow
	balances := map[string]model.Amount{}
	for i, tx := range txs {
		e := entries[i]
		if re == nil {
			rows = append(rows, registerRow{
				ID: tx.ID, Date: tx.Date, Payee: payeeOf(tx, e),
				Amount: tx.Amount, Account: e.SourceAccount(tx), Door: doors[tx.ID],
			})
			continue
		}

		// The movements this line made on the matched accounts, netted per commodity.
		type movement struct {
			amount   model.Amount
			accounts []string
		}
		var order []string
		byCommodity := map[string]*movement{}
		add := func(account string, amount model.Amount) error {
			m, ok := byCommodity[amount.Commodity]
			if !ok {
				m = &movement{amount: model.Amount{Commodity: amount.Commodity}}
				byCommodity[amount.Commodity] = m
				order = append(order, amount.Commodity)
			}
			sum, err := m.amount.Add(amount)
			if err != nil {
				return err
			}
			m.amount = sum
			if len(m.accounts) == 0 || m.accounts[len(m.accounts)-1] != account {
				m.accounts = append(m.accounts, account)
			}
			return nil
		}
		if src := e.SourceAccount(tx); re.MatchString(src) {
			if err := add(src, tx.Amount); err != nil {
				return nil, err
			}
		}
		for _, p := range e.Postings {
			if re.MatchString(p.Account) {
				if err := add(p.Account, p.Amount); err != nil {
					return nil, err
				}
			}
		}

		for _, commodity := range order {
			m := byCommodity[commodity]
			if m.amount.IsZero() {
				continue
			}
			bal, ok := balances[commodity]
			if !ok {
				bal = model.Amount{Commodity: commodity}
			}
			bal, err := bal.Add(m.amount)
			if err != nil {
				return nil, err
			}
			balances[commodity] = bal
			rows = append(rows, registerRow{
				ID: tx.ID, Date: tx.Date, Payee: payeeOf(tx, e),
				Amount: m.amount, Balance: bal,
				Account: joined(m.accounts), Door: doors[tx.ID],
			})
		}
	}
	return rows, nil
}

// payeeOf names the line for a statement reading: the entry's payee, or the raw memo where the
// rules have not named one, because a statement row with no text cannot be recognized.
func payeeOf(tx model.Transaction, e model.Entry) string {
	if e.Payee != "" {
		return e.Payee
	}
	return tx.Description
}

func joined(accounts []string) string {
	out := accounts[0]
	for _, a := range accounts[1:] {
		out += " + " + a
	}
	return out
}

// renderRegister prints the statement. The balance column appears only on a filtered register,
// where it is one account's balance running down the page the way the bank prints it.
func renderRegister(out io.Writer, rows []registerRow, balanced bool) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if balanced {
		fmt.Fprintln(w, "DATE\tFINGERPRINT\tPAYEE\tAMOUNT\tBALANCE\tACCOUNT\tDOOR")
	} else {
		fmt.Fprintln(w, "DATE\tFINGERPRINT\tPAYEE\tAMOUNT\tACCOUNT\tDOOR")
	}
	for _, r := range rows {
		if balanced {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Date.Format("2006-01-02"), r.ID, r.Payee, r.Amount, r.Balance, r.Account, r.Door)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Date.Format("2006-01-02"), r.ID, r.Payee, r.Amount, r.Account, r.Door)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\n%d lines\n", len(rows))
	return err
}

// twinRow is one side of a candidate twin: enough to judge whether the two lines are one purchase.
type twinRow struct {
	ID      string
	Payee   string
	Door    string
	Account string
	PostsTo string
}

// twinGroup is one date+amount collision that crossed doors: the lines that might be a single
// real-world transaction entered more than once.
type twinGroup struct {
	Date   time.Time
	Amount model.Amount
	Rows   []twinRow
}

// twinGroups finds the candidate twins: lines sharing a date and an amount that the books cannot catch
// on their own. Two doors colliding is the classic case -- fingerprints dedupe within a door, and
// reconcile compares each door's lines to its own bank -- so a purchase entering through two doors
// doubles silently.
//
// One door colliding with itself is reported too, but only when the memos differ. A door does dedupe
// itself, yet it does so by a fingerprint taken over the memo, so the moment a bank changes how it
// writes a line it has already served -- a truncated "Online Banking payment" arriving later as
// "Online Banking payment - 7604 PROV NB PROP TX" -- the dedupe misses and the line lands twice
// through the one door. What the same-door exemption is really there to protect is a bank charging
// the same amount twice in a day, and that case says the same memo both times; requiring the memos to
// differ keeps those out while catching drift.
//
// A settled accrual and the line that paid it are one recorded flow, not a coincidence, so
// settlements says which collisions the books have already explained. Voided lines and paired
// transfers have already left the fold this reads.
func twinGroups(txs []model.Transaction, entries []model.Entry, doors, settlements map[string]string) []twinGroup {
	var order []string
	byKey := map[string][]int{}
	for i, tx := range txs {
		key := tx.Date.Format("2006-01-02") + "|" + amountKey(tx.Amount)
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], i)
	}

	var groups []twinGroup
	for _, key := range order {
		idxs := unsettled(byKey[key], txs, settlements)
		if len(idxs) < 2 {
			continue
		}
		suspect := false
		for _, i := range idxs[1:] {
			if doors[txs[i].ID] != doors[txs[idxs[0]].ID] ||
				txs[i].Description != txs[idxs[0]].Description {
				suspect = true
				break
			}
		}
		if !suspect {
			continue
		}
		g := twinGroup{Date: txs[idxs[0]].Date, Amount: txs[idxs[0]].Amount}
		for _, i := range idxs {
			g.Rows = append(g.Rows, twinRow{
				ID: txs[i].ID, Payee: payeeOf(txs[i], entries[i]), Door: doors[txs[i].ID],
				Account: entries[i].SourceAccount(txs[i]), PostsTo: accounts(entries[i]),
			})
		}
		groups = append(groups, g)
	}
	return groups
}

// unsettled drops, from one collision's lines, each settled accrual whose paying line is also in the
// collision -- and the payer with it. The books recorded that pairing on purpose (invoice settle), so
// offering it back as a suspected double every month would teach the reader to ignore the report.
func unsettled(idxs []int, txs []model.Transaction, settlements map[string]string) []int {
	member := make(map[string]bool, len(idxs))
	for _, i := range idxs {
		member[txs[i].ID] = true
	}
	paired := map[string]bool{}
	for _, i := range idxs {
		if payer, ok := settlements[txs[i].ID]; ok && member[payer] {
			paired[txs[i].ID] = true
			paired[payer] = true
		}
	}
	if len(paired) == 0 {
		return idxs
	}
	kept := make([]int, 0, len(idxs))
	for _, i := range idxs {
		if !paired[txs[i].ID] {
			kept = append(kept, i)
		}
	}
	return kept
}

// amountKey is an amount's identity for the twin fold: the same value written with a different
// number of decimal places is the same amount, so trailing zeros are trimmed before comparing.
func amountKey(a model.Amount) string {
	units, scale := a.Units, a.Scale
	for scale > 0 && units%10 == 0 {
		units /= 10
		scale--
	}
	return fmt.Sprintf("%d@%d %s", units, scale, a.Commodity)
}

// renderTwins prints each collision as a block: the shared date and amount, then each line with its
// door and categorization, so judging "one purchase or two?" is reading a short list. The report
// only ever suggests; a twin that is real is voided by hand, one side at a time.
func renderTwins(out io.Writer, groups []twinGroup) error {
	if len(groups) == 0 {
		_, err := fmt.Fprintln(out, "no candidate twins: no date+amount collision crosses doors")
		return err
	}
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  %s\n", g.Date.Format("2006-01-02"), g.Amount)
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, r := range g.Rows {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", r.ID, r.Door, r.Account, r.Payee, r.PostsTo)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "\n%d candidate twin group(s); a twin that is real is voided on one side (bkpr void <fingerprint>)\n", len(groups))
	return err
}
