package books

import (
	"encoding/json"

	"github.com/dallasread/bookkeeper/lib/eventlog"
)

// ActionVoided records that a recorded thing was annulled: a bad import thrown out, or an invoice
// that should not have been raised. It is one verb across every document you can record and then
// take back, because it is one operation — the noun says which kind of thing, and the why says why.
//
// Like every fact here it does not delete. It appends a fact that supersedes the earlier one, and
// the fold drops the record, so the mistake and its correction both survive in the log.
const ActionVoided = "voided"

// voidedData is the payload of a *.voided event: the reason the thing was annulled. The reason lives
// here, not in the verb, so "a bad import" and "the client cancelled" are the same event with
// different whys rather than two different event names.
type voidedData struct {
	Why string `json:"why,omitempty"`
}

// VoidTransaction annuls an imported line, which is how a bad import is undone in an append-only log.
// A source stores its line already normalized, so a bad mapping's output cannot be repaired by
// fixing the mapping and re-importing: the fingerprints are already present and re-import is a no-op.
// Voiding is the way out. It must be a line the books currently show, so a fingerprint that was never
// imported (or is already voided) is refused rather than recorded against nothing.
func VoidTransaction(log *eventlog.Log, actor, why, txID string) error {
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(voidedData{Why: why})
	if err != nil {
		return err
	}
	// tx.ID, not txID: the caller may have quoted a prefix, and the void must key to the line.
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: tx.ID, Action: ActionVoided,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// voidedTransactions folds the log into the set of transaction fingerprints that have been voided.
func voidedTransactions(events []eventlog.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionTransaction && e.Action == ActionVoided {
			out[e.RecordID] = true
		}
	}
	return out
}
