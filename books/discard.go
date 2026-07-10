package books

import (
	"encoding/json"

	"github.com/dallasread/bookkeeper/eventlog"
)

// ActionDiscarded records that an imported line was garbage and must leave the books.
//
// An append-only log cannot edit or delete, and a source stores its line already normalized, so a
// bad mapping's output cannot be repaired by fixing the mapping and re-importing: the fingerprints
// are already present and re-import is a no-op. Discard is the way out. It does not remove the
// imported event; it appends a fact that supersedes it, and the fold skips the line. The mistake
// and its correction both survive in the log.
const ActionDiscarded = "discarded"

type discardedData struct {
	Why string `json:"why,omitempty"`
}

// Discard removes a line from the books. It must be a line the books currently show, so a
// fingerprint that was never imported (or is already discarded) is refused rather than recorded
// against nothing.
func Discard(log *eventlog.Log, actor, why, txID string) error {
	if _, err := Transaction(log, txID); err != nil {
		return err
	}

	data, err := json.Marshal(discardedData{Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionDiscarded,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// discarded folds the log into the set of fingerprints that have been discarded.
func discarded(events []eventlog.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionTransaction && e.Action == ActionDiscarded {
			out[e.RecordID] = true
		}
	}
	return out
}
