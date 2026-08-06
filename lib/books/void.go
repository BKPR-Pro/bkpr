package books

import (
	"encoding/json"
	"fmt"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
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

// ActionUnvoided is the inverse of ActionVoided: a later fact that supersedes a void and restores
// the line to the books, the way any other correction here does. void's own help text promised a
// later fact could supersede it; before this there was no fact that did.
const ActionUnvoided = "unvoided"

// unvoidedData is the payload of a *.unvoided event: the reason the void is being reversed.
type unvoidedData struct {
	Why string `json:"why,omitempty"`
}

// UnvoidTransaction reverses a void, restoring the line to the books as though it had never been
// voided. It must be a line that is currently voided; a fingerprint that was never voided, or was
// never imported at all, is refused rather than recorded against nothing.
func UnvoidTransaction(log *eventlog.Log, actor, why, txID string) error {
	events, err := log.All()
	if err != nil {
		return err
	}

	id, ok, err := resolveImportedID(events, txID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("books: no transaction %q; import it before unvoiding it", txID)
	}
	if !voidedTransactions(events)[id] {
		return fmt.Errorf("books: %q is not voided", id)
	}

	data, err := json.Marshal(unvoidedData{Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: id, Action: ActionUnvoided,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// voidedTransactions folds the log into the set of transaction fingerprints currently voided:
// later wins, so a void followed by an unvoid drops back out of the set.
func voidedTransactions(events []eventlog.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Collection != CollectionTransaction {
			continue
		}
		switch e.Action {
		case ActionVoided:
			out[e.RecordID] = true
		case ActionUnvoided:
			out[e.RecordID] = false
		}
	}
	return out
}

// resolveImportedID resolves a fingerprint or unique prefix to the id of a line that was imported
// at some point, voided or not. Transaction (in books.go) only sees what the current projection
// still shows, which hides a voided line; unvoiding needs to find it anyway, straight off the
// import facts.
func resolveImportedID(events []eventlog.Event, key string) (string, bool, error) {
	var ids []string
	seen := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionTransaction && e.Action == ActionImported && !seen[e.RecordID] {
			seen[e.RecordID] = true
			ids = append(ids, e.RecordID)
		}
	}
	return byPrefix(ids, key, func(id string) string { return id })
}
