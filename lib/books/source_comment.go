package books

import (
	"encoding/json"
	"fmt"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

// ActionCommented records a free-text note on a transaction's own (elided, source) leg: the
// balancing posting the ledger infers rather than spells out. The note is commentary, not data --
// it takes no part in balancing or the fingerprint -- so it is its own small fact rather than a
// field baked into the import, and the latest one for a line wins. That is what lets a carried-in
// note and a hand-written one be the same event, and lets either be corrected without touching the
// import that can never be rewritten. An empty note clears an earlier one.
const ActionCommented = "commented"

// commentedData is the payload of a transaction.commented event: the note on the source leg, and why
// it was left. An empty Comment clears a note recorded earlier.
type commentedData struct {
	Comment string `json:"comment,omitempty"`
	Why     string `json:"why,omitempty"`
}

// commentSource records a note on one line's source leg, keyed by its fingerprint. It is the shared
// tail of a carried-in note the import found in a file and a hand-written one the comment command
// leaves: both are the same fact -- what the elided leg means -- and the latest wins.
func commentSource(log *eventlog.Log, actor, why, txID, text string) error {
	data, err := json.Marshal(commentedData{Comment: text, Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionCommented,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// sourceComments folds the log into the note each line's source leg currently carries, latest wins.
// A line with no commented event is absent from the map; one whose latest note is empty maps to "",
// which is how a cleared note reads, so the caller distinguishes "never noted" from "note removed".
func sourceComments(events []eventlog.Event) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range events {
		if e.Collection != CollectionTransaction || e.Action != ActionCommented {
			continue
		}
		var data commentedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out[e.RecordID] = data.Comment
	}
	return out, nil
}
