package books

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// An accrual is value recognized before its cash: an invoice (money owed to you) or a bill (money
// you owe). Both book a line when the value is earned or incurred and clear a parked account when
// the cash finally moves. They differ only in signs and in the words their events use — an invoice
// is raised and debits a receivable; a bill is received and credits a payable — so the machinery
// they share lives here, and each keeps only its own nouns.

// ActionSettled links an accrual to the bank line that paid it. It is one verb across invoices and
// bills, because clearing a receivable and clearing a payable are one operation; the collection says
// which. The memo of a deposit does not reliably name which accrual it clears, so the pairing is
// recorded rather than guessed, unlike an internal transfer where each sighting names the other's
// account. A later settle supersedes, and an empty one reopens.
const ActionSettled = "settled"

// accrualData is the payload of an invoice.raised or bill.received event: everything the fold needs
// to rebuild the accrual and render its line.
type accrualData struct {
	Date     time.Time    `json:"date"`
	Party    string       `json:"party"`
	Amount   model.Amount `json:"amount"`
	Category string       `json:"category"`
	Account  string       `json:"account"`
	Why      string       `json:"why,omitempty"`
}

type settledData struct {
	Tx string `json:"tx,omitempty"` // the bank line that paid it; empty reopens the accrual
}

// accrualLine is the shape the overlay renders, folded from either an invoice or a bill. parkedAmount
// is signed from the parked account's own point of view: a receivable is an asset, so an invoice adds
// a positive amount; a payable is a liability, so a bill adds a negative one. Everything else is the
// same, which is why one overlay books both.
type accrualLine struct {
	kind          string // "invoice" or "bill", the prefix that keeps the synthetic id unique
	id            string // the accrual's fingerprint
	date          time.Time
	party         string
	parkedAccount string       // the receivable (invoice) or payable (bill)
	parkedAmount  model.Amount // signed from the parked account's view: +magnitude for AR, -magnitude for AP
	category      string       // the income (invoice) or expense (bill) account
	settledBy     string       // the bank line that paid it, or "" while open
}

// transaction and entry render the accrual as a synthetic balanced line, so the ledger writer that
// renders a bank statement renders an accrual with no special case. The parked account is the elided
// one, exactly as a statement's own account is.
func (a accrualLine) transaction() model.Transaction {
	return model.Transaction{
		ID:          a.kind + ":" + a.id,
		Account:     a.parkedAccount,
		Date:        a.date,
		Amount:      a.parkedAmount,
		Description: a.party,
	}
}

func (a accrualLine) entry() model.Entry {
	return model.Entry{Payee: a.party, Postings: []model.Posting{{Account: a.category, Amount: a.parkedAmount.Negate()}}}
}

// accrualLines folds every open invoice and bill into the shared shape, in raise/receive order.
func accrualLines(log *eventlog.Log) ([]accrualLine, error) {
	invoiceLines, err := invoiceLines(log)
	if err != nil {
		return nil, err
	}
	billLines, err := billLines(log)
	if err != nil {
		return nil, err
	}
	return append(invoiceLines, billLines...), nil
}

// overlayAccruals turns the cash-basis books into accrual-basis ones. It books each open invoice and
// bill as its own line, and where a bank line settled one it redirects that line from income or
// expense to the parked account, so the cash clears the receivable or payable instead of
// double-booking the value the accrual already recognized. The combined lines are re-sorted so
// accruals interleave in date order.
func overlayAccruals(log *eventlog.Log, txs []model.Transaction, entries []model.Entry, since time.Time) ([]model.Transaction, []model.Entry, error) {
	lines, err := accrualLines(log)
	if err != nil {
		return nil, nil, err
	}

	// An effective date bounds the switch to accrual: only accruals dated on or after it are booked,
	// and an earlier one is dropped whole — no synthetic line, and no redirect of a settling payment,
	// so a receivable that predates the switch reads exactly as cash (its payment books as income when
	// it lands). Dropping it from lines here handles both, since nothing downstream sees it.
	if !since.IsZero() {
		kept := make([]accrualLine, 0, len(lines))
		for _, ln := range lines {
			if !ln.date.Before(since) {
				kept = append(kept, ln)
			}
		}
		lines = kept
	}

	pos := make(map[string]int, len(txs))
	for i, tx := range txs {
		pos[tx.ID] = i
	}
	for _, ln := range lines {
		if ln.settledBy == "" {
			continue
		}
		i, ok := pos[ln.settledBy]
		if !ok {
			continue // the line was voided or suppressed; nothing to clear against
		}
		entries[i] = model.Entry{
			Payee:    entries[i].Payee,
			Postings: []model.Posting{{Account: ln.parkedAccount, Amount: txs[i].Amount.Negate()}},
		}
	}

	for _, ln := range lines {
		txs = append(txs, ln.transaction())
		entries = append(entries, ln.entry())
	}

	sortByDate(txs, entries)
	return txs, entries, nil
}

// settlementWindow is how far from an accrual's date a bank line may fall and still be offered as
// the one that settled it. It is generous, because an invoice can sit unpaid for months; the offer
// is only a suggestion, so a wide net costs nothing.
const settlementWindow = 180

// SettlementCandidates returns, for each open accrual, the bank lines that plausibly settled it: a
// real imported line whose amount equals the parked amount (so a deposit clears a receivable and a
// payment clears a payable), dated within the window, and not already settling another accrual. It
// is keyed by the accrual's fingerprint, the same id `invoice list` and `bill list` print.
//
// This is the transfer-pairing heuristic surfaced rather than applied. A deposit's memo does not
// prove which invoice it clears, so the pairing stays an offer the person confirms with `settle`,
// never a guess the fold makes. The point is only to spare the fingerprint-hunting: pick from a
// short list instead of grepping the log.
func SettlementCandidates(log *eventlog.Log) (map[string][]string, error) {
	lines, err := accrualLines(log)
	if err != nil {
		return nil, err
	}
	txs, err := Transactions(log)
	if err != nil {
		return nil, err
	}

	// A line already settling some accrual is spoken for, so it is never offered for another.
	used := map[string]bool{}
	for _, ln := range lines {
		if ln.settledBy != "" {
			used[ln.settledBy] = true
		}
	}

	out := map[string][]string{}
	for _, ln := range lines {
		if ln.settledBy != "" {
			continue // already settled; nothing to offer
		}
		for _, tx := range txs {
			if used[tx.ID] {
				continue
			}
			if !tx.Amount.Equal(ln.parkedAmount) {
				continue // a deposit clears a receivable, a payment clears a payable, sign and all
			}
			if daysApart(tx.Date, ln.date) > settlementWindow {
				continue
			}
			out[ln.id] = append(out[ln.id], tx.ID)
		}
	}
	return out, nil
}

// AgedAccrual is one open accrual placed in an aging bucket as of a date: how long the money has
// been owed, and which band that falls in. It is what an AR (invoices) or AP (bills) aging report is
// built from.
type AgedAccrual struct {
	ID     string
	Date   time.Time
	Party  string
	Amount model.Amount // the positive magnitude owed
	Days   int          // as-of minus the accrual's date
	Bucket string       // the aging band: current, 31-60, 61-90, or 90+
}

// InvoiceAging ages the open invoices as of a date: the receivables you are still owed, oldest
// first, each in its bucket. Settled and voided invoices are already gone from the fold.
func InvoiceAging(log *eventlog.Log, asOf time.Time) ([]AgedAccrual, error) {
	lines, err := invoiceLines(log)
	if err != nil {
		return nil, err
	}
	return aged(lines, asOf), nil
}

// BillAging ages the open bills as of a date: the payables you still owe, the mirror of InvoiceAging.
func BillAging(log *eventlog.Log, asOf time.Time) ([]AgedAccrual, error) {
	lines, err := billLines(log)
	if err != nil {
		return nil, err
	}
	return aged(lines, asOf), nil
}

// aged buckets the open lines by how long they have been outstanding, oldest first, so the report is
// stable and reads top-down from the most overdue.
func aged(lines []accrualLine, asOf time.Time) []AgedAccrual {
	var out []AgedAccrual
	for _, ln := range lines {
		if ln.settledBy != "" {
			continue // only what is still owed ages
		}
		amount := ln.parkedAmount
		if amount.Units < 0 {
			amount = amount.Negate() // a payable is stored negative; aging shows the magnitude owed
		}
		days := int(asOf.Sub(ln.date).Hours()) / 24
		out = append(out, AgedAccrual{ID: ln.id, Date: ln.date, Party: ln.party, Amount: amount, Days: days, Bucket: bucketOf(days)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// bucketOf names the aging band a number of days outstanding falls in. The 30/60/90 bands are the
// ones every aging report uses, so the output reads the way an accountant expects.
func bucketOf(days int) string {
	switch {
	case days <= 30:
		return "current"
	case days <= 60:
		return "31-60"
	case days <= 90:
		return "61-90"
	default:
		return "90+"
	}
}

// trackSettlement records that a bank line paid an accrual, or reopens it when txID is empty.
func trackSettlement(log *eventlog.Log, actor, collection, recordID, txID string) error {
	data, err := json.Marshal(settledData{Tx: txID})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: collection, RecordID: recordID, Action: ActionSettled,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// settlementsFor folds the current bank line, if any, that settles each accrual in a collection. A
// later settle supersedes, an empty one reopens, so the map holds only accruals still linked.
func settlementsFor(log *eventlog.Log, collection string) (map[string]string, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range events {
		if e.Collection != collection || e.Action != ActionSettled {
			continue
		}
		var d settledData
		if err := e.Decode(&d); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		if d.Tx == "" {
			delete(out, e.RecordID)
			continue
		}
		out[e.RecordID] = d.Tx
	}
	return out, nil
}

// trackVoid records that an accrual should not have been raised or received. Voiding is one verb
// across every recorded thing (see ActionVoided in void.go); the collection says which noun.
func trackVoid(log *eventlog.Log, actor, collection, why, recordID string) error {
	data, err := json.Marshal(voidedData{Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: collection, RecordID: recordID, Action: ActionVoided,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// accrualFingerprint hashes an accrual by its content, the way a statement line is fingerprinted, so
// recording the same one twice collapses to one fact. The kind is in the hash so an invoice and a
// bill that happen to share fields still take different ids.
func accrualFingerprint(kind string, date time.Time, party string, amount model.Amount, category, account string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		kind,
		date.Format("2006-01-02"),
		strings.Join(strings.Fields(strings.ToLower(party)), " "),
		amount.String(),
		category,
		account,
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// sortByDate orders the lines and their entries together, by date and then id, so the artifact is
// byte-stable across runs the way Transactions already keeps the cash-basis lines.
func sortByDate(txs []model.Transaction, entries []model.Entry) {
	idx := make([]int, len(txs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		i, j := idx[a], idx[b]
		if !txs[i].Date.Equal(txs[j].Date) {
			return txs[i].Date.Before(txs[j].Date)
		}
		return txs[i].ID < txs[j].ID
	})
	st := make([]model.Transaction, len(txs))
	se := make([]model.Entry, len(entries))
	for n, i := range idx {
		st[n], se[n] = txs[i], entries[i]
	}
	copy(txs, st)
	copy(entries, se)
}
