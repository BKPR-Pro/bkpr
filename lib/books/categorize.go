package books

import (
	"encoding/json"
	"fmt"

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

// Ledger folds the whole log into transactions and their final entries, in date order. Each line
// is categorized by the rules, then overridden by the latest human or model assertion for that
// specific line. This is the pipeline the table and the ledger artifact both render.
func Ledger(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
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

	// The duplicate sighting of an internal transfer must not book a second entry, so it is dropped
	// from the books entirely rather than rendered.
	dup := suppressed(txs, entries)
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
		out[e.RecordID] = model.Entry{Payee: data.Payee, Postings: data.Postings}
	}
	return out, nil
}
