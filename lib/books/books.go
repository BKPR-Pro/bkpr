// Package books holds the commands that write facts and the projections that fold them back.
//
// A command captures one intent and emits one event. A projection rebuilds state from the log on
// read, so nothing derived is ever stored: the books are a pure function of the log, which is what
// makes regenerating them boring and a changed diff line meaningful.
package books

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

const (
	// CollectionTransaction is keyed by a transaction's fingerprint.
	CollectionTransaction = "transaction"

	// ActionImported records that a statement line was read in. It can only be true once.
	ActionImported = "imported"

	version = 1
)

// importedData is the payload of a transaction.imported event. The transaction is stored already
// normalized, because a source is a door rather than a fold: re-reading the log never re-parses a
// CSV, and re-normalizing would move every fingerprint and orphan every correction keyed to one.
type importedData struct {
	Account     string            `json:"account"`
	Date        time.Time         `json:"date"`
	Amount      model.Amount      `json:"amount"`
	Description string            `json:"description"`
	Raw         map[string]string `json:"raw,omitempty"`
}

// ImportResult reports what one statement did to the log.
type ImportResult struct {
	Imported int
	Skipped  int // already in the log, because statements overlap
}

// Import records each statement line exactly once, keyed by its fingerprint. A line already in
// the log is skipped, so re-importing an overlapping statement is a no-op rather than a second
// rent payment.
func Import(log *eventlog.Log, actor string, txs []model.Transaction) (ImportResult, error) {
	var result ImportResult

	// The log has no transaction, so a bad line halfway through a statement would leave the earlier
	// ones written. Refuse the whole statement before writing any of it.
	for _, tx := range txs {
		if tx.ID == "" {
			return result, fmt.Errorf("books: %s on %s has no fingerprint",
				tx.Description, tx.Date.Format("2006-01-02"))
		}
	}

	for _, tx := range txs {
		data, err := json.Marshal(importedData{
			Account:     tx.Account,
			Date:        tx.Date,
			Amount:      tx.Amount,
			Description: tx.Description,
			Raw:         tx.Raw,
		})
		if err != nil {
			return result, fmt.Errorf("books: %s: %w", tx.ID, err)
		}

		_, err = log.TrackOnce(eventlog.Event{
			Collection: CollectionTransaction,
			RecordID:   tx.ID,
			Action:     ActionImported,
			Version:    version,
			Actor:      actor,
			Data:       data,
		})
		switch {
		case errors.Is(err, eventlog.ErrAlreadyTracked):
			result.Skipped++
		case err != nil:
			return result, fmt.Errorf("books: %s: %w", tx.ID, err)
		default:
			result.Imported++
			// A note on the source leg is a separate fact from the import, latest-wins, so it is carried
			// as its own event rather than baked in. Only on a fresh import: a re-import is a no-op, and
			// re-carrying the file's note would clobber a note the comment command left in the meantime.
			if tx.Comment != "" {
				if err := commentSource(log, actor, "", tx.ID, tx.Comment); err != nil {
					return result, fmt.Errorf("books: %s: %w", tx.ID, err)
				}
			}
		}
	}
	return result, nil
}

// Transactions folds the log back into the lines the bank reported, in date order. A ledger reads
// best that way, and statements arrive in whatever order they arrive.
func Transactions(log *eventlog.Log) ([]model.Transaction, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	gone := voidedTransactions(events)
	comments, err := sourceComments(events)
	if err != nil {
		return nil, err
	}

	var txs []model.Transaction
	for _, e := range events {
		if e.Collection != CollectionTransaction || e.Action != ActionImported {
			continue
		}
		if gone[e.RecordID] {
			continue // voided: the imported fact stays in the log, but the line leaves the books
		}
		var data importedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		txs = append(txs, model.Transaction{
			ID:          e.RecordID,
			Account:     data.Account,
			Date:        data.Date,
			Amount:      data.Amount,
			Description: data.Description,
			Raw:         data.Raw,
			Comment:     comments[e.RecordID],
		})
	}

	// The fingerprint breaks ties, so the order is total and the artifact is stable across runs.
	sort.SliceStable(txs, func(i, j int) bool {
		if !txs[i].Date.Equal(txs[j].Date) {
			return txs[i].Date.Before(txs[j].Date)
		}
		return txs[i].ID < txs[j].ID
	})
	return txs, nil
}

// Transaction folds out the one imported line whose fingerprint is id or uniquely begins with it,
// so a fingerprint read off a listing can be quoted by a prefix, as a git hash can.
func Transaction(log *eventlog.Log, id string) (model.Transaction, error) {
	txs, err := Transactions(log)
	if err != nil {
		return model.Transaction{}, err
	}
	tx, ok, err := byPrefix(txs, id, func(tx model.Transaction) string { return tx.ID })
	if err != nil {
		return model.Transaction{}, err
	}
	if !ok {
		return model.Transaction{}, fmt.Errorf("books: no transaction %q; import it before categorizing it", id)
	}
	return tx, nil
}
