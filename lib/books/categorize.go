package books

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
)

// ActionCategorized records that a person or a model asserted the postings for one transaction.
// It is keyed by the transaction's fingerprint and overrides whatever the rules would have said
// for that one line. It never generalizes, because the attribution is real-world context the
// description does not contain.
const ActionCategorized = "categorized"

type categorizedData struct {
	Payee    string          `json:"payee"`
	Postings []model.Posting `json:"postings"`
	Gain     string          `json:"gain,omitempty"` // set on a sale: the account its capital gain lands in
	Why      string          `json:"why,omitempty"`
}

// Categorize asserts the postings for one imported transaction.
//
// The line must exist, so the fingerprint cannot orphan, and the postings must account for the
// whole line, so the books stay balanced. Both are checked here rather than left for the render:
// the books are the artifact, and a bad assertion should be refused at the moment it is made.
func Categorize(log *eventlog.Log, actor, why, txID, payee string, postings []model.Posting) error {
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}

	entry := model.Entry{Payee: payee, Postings: postings}
	if !entry.Balances(tx) {
		return fmt.Errorf("books: the postings do not account for %s", tx.Amount.Negate())
	}

	data, err := json.Marshal(categorizedData{Payee: payee, Postings: postings, Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionCategorized,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// Ledger folds the whole log into transactions and their final entries, in date order, on the cash
// basis. Each line is categorized by the rules, then overridden by the latest human or model
// assertion for that specific line. This is the pipeline the table and the ledger artifact both
// render, and the basis the tool was born on: every line is money that actually moved.
func Ledger(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	return LedgerBasis(log, CashBasis)
}

// Basis is the lens the books are read through. Cash records only money that moved; accrual also
// books the revenue that was earned before it. Both are folds over the one log: the basis is chosen
// at read time, never stored, so a book of statements can be read either way and switched between
// them without rewriting anything.
type Basis string

const (
	CashBasis    Basis = "cash"
	AccrualBasis Basis = "accrual"
)

// LedgerBasis folds the log through the chosen lens. On the cash basis it is Ledger: only money that
// moved. On the accrual basis it also books every open invoice and bill as its own line and lets a
// settling bank line clear the receivable or payable it raised. The basis is a read-time choice over
// one log, so the same books can be read either way, and a set of books can switch between them
// without any rewrite: the accrual lines simply appear or fall away.
func LedgerBasis(log *eventlog.Log, basis Basis) ([]model.Transaction, []model.Entry, error) {
	return LedgerBasisSince(log, basis, time.Time{})
}

// LedgerBasisSince is LedgerBasis with an effective date for the switch to accrual: on the accrual
// basis only invoices and bills dated on or after since are booked, so a book can turn on accrual
// mid-year without retroactively accruing everything before it. A zero since books all of them. The
// date is a read-time argument, never stored, so the seam it creates is a fact about how you are
// reading the log, not a change to the log. An accrual dated before since is treated exactly as cash:
// its payment books as ordinary income or expense when it lands.
func LedgerBasisSince(log *eventlog.Log, basis Basis, since time.Time) ([]model.Transaction, []model.Entry, error) {
	txs, entries, err := cashLedger(log)
	if err != nil {
		return nil, nil, err
	}
	if basis == AccrualBasis {
		return overlayAccruals(log, txs, entries, since)
	}
	return txs, entries, nil
}

// cashLedger folds the log into the lines the bank reported and their final entries, in date order.
func cashLedger(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	txs, entries, err := categorized(log)
	if err != nil {
		return nil, nil, err
	}

	// A sale asserts only which shares left; the cost base they carry, and so the gain, is folded
	// from the account's history here rather than stored on the assertion.
	if err := resolveDisposals(txs, entries); err != nil {
		return nil, nil, err
	}

	// A manual match overrides the automatic pairing; it is folded here and applied by suppressed.
	overrides, err := matches(log)
	if err != nil {
		return nil, nil, err
	}

	// The duplicate sighting of an internal transfer must not book a second entry, so it is dropped
	// from the books entirely rather than rendered.
	dup := suppressed(txs, entries, overrides)
	keptTxs := make([]model.Transaction, 0, len(txs))
	keptEntries := make([]model.Entry, 0, len(entries))
	for i, tx := range txs {
		if dup[tx.ID] {
			continue
		}
		keptTxs = append(keptTxs, tx)
		keptEntries = append(keptEntries, entries[i])
	}
	return keptTxs, keptEntries, nil
}

// categorized folds the log into every transaction and the entry it currently carries, in date
// order, before disposals are priced and transfers suppressed. Each line is categorized by the
// rules, then overridden by the latest human or model assertion for that specific line. It is the
// shared front half of Ledger, reused to validate a sale before it is recorded.
func categorized(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	set, err := Rules(log)
	if err != nil {
		return nil, nil, err
	}
	engine, err := rules.New(set)
	if err != nil {
		return nil, nil, err
	}

	txs, err := Transactions(log)
	if err != nil {
		return nil, nil, err
	}

	asserted, err := assertions(log)
	if err != nil {
		return nil, nil, err
	}

	entries := make([]model.Entry, len(txs))
	for i, tx := range txs {
		if entry, ok := asserted[tx.ID]; ok {
			entries[i] = entry
			continue
		}
		entries[i] = engine.Apply(tx)
	}
	return txs, entries, nil
}

// assertions folds the categorized events into the current entry per transaction. A later event
// about the same line replaces an earlier one, so the map simply takes each in log order.
func assertions(log *eventlog.Log) (map[string]model.Entry, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	out := map[string]model.Entry{}
	for _, e := range events {
		if e.Collection != CollectionTransaction || e.Action != ActionCategorized {
			continue
		}
		var data categorizedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out[e.RecordID] = model.Entry{Payee: data.Payee, Postings: data.Postings, Gain: data.Gain}
	}
	return out, nil
}
