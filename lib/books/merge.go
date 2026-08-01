package books

import (
	"encoding/json"
	"errors"
	"fmt"

	"bkpr.pro/bkpr/lib/eventlog"
)

const (
	// CollectionBook holds facts about the book itself. Its one action so far records that another
	// book's log was merged in, keyed by a fingerprint of that file's content, so re-importing the
	// same file is a no-op the way re-importing a statement is.
	CollectionBook = "book"
	ActionMerged   = "merged"
)

// mergedData is the payload of a book.merged event: how much the merged file held, for the record.
type mergedData struct {
	Events int `json:"events"`
}

// onceOnly names the facts TrackOnce guards, so a merge replays them through the same gate and a
// line, invoice, bill, or export both books saw lands exactly once.
var onceOnly = map[[2]string]bool{
	{CollectionTransaction, ActionImported}: true,
	{CollectionTransaction, ActionExported}: true,
	{CollectionInvoice, ActionRaised}:       true,
	{CollectionBill, ActionReceived}:        true,
}

// MergeResult reports what merging another book's log did.
type MergeResult struct {
	Recorded int  // facts recorded into this book
	Skipped  int  // already known here, keyed by fingerprint
	Repeat   bool // this whole file was merged before; nothing was recorded
}

// MergeLog replays another book's events into this one, in their order. The log is its own
// interchange format, so combining two books is an import rather than a new serialization.
//
// Once-only facts (statement lines, invoices, bills, exports) dedupe by fingerprint. Recurring
// facts (rules, corrections, matches, settlements) are recorded again here, later than everything
// this book already holds — so where both books answered the same question, the imported book's
// answer wins, and the fold makes a rule pattern both books authored one rule rather than two.
// A transfer each book saw from its own side pairs up in the fold once both sides are present.
//
// sourceID fingerprints the merged file's content. The marker is recorded last, so a merge that
// failed midway is retried rather than skipped; on the retry the once-only facts dedupe and a
// recurring fact recorded twice folds to the same books.
func MergeLog(log *eventlog.Log, actor, sourceID string, events []eventlog.Event) (MergeResult, error) {
	existing, err := log.All()
	if err != nil {
		return MergeResult{}, err
	}
	for _, e := range existing {
		if e.Collection == CollectionBook && e.Action == ActionMerged && e.RecordID == sourceID {
			return MergeResult{Skipped: len(events), Repeat: true}, nil
		}
	}

	var res MergeResult
	for _, e := range events {
		// The other book's own merge markers say what it merged, not what this book has; carrying
		// them over could silently skip a file this book has genuinely never seen.
		if e.Collection == CollectionBook && e.Action == ActionMerged {
			res.Skipped++
			continue
		}

		replay := eventlog.Event{
			Collection: e.Collection, RecordID: e.RecordID, Action: e.Action,
			Version: e.Version, Actor: e.Actor, Data: e.Data,
		}
		if onceOnly[[2]string{e.Collection, e.Action}] {
			_, err := log.TrackOnce(replay)
			switch {
			case errors.Is(err, eventlog.ErrAlreadyTracked):
				res.Skipped++
			case err != nil:
				return res, fmt.Errorf("books: merging %s %s.%s: %w", e.Collection, e.RecordID, e.Action, err)
			default:
				res.Recorded++
			}
			continue
		}
		if _, err := log.Track(replay); err != nil {
			return res, fmt.Errorf("books: merging %s %s.%s: %w", e.Collection, e.RecordID, e.Action, err)
		}
		res.Recorded++
	}

	data, err := json.Marshal(mergedData{Events: len(events)})
	if err != nil {
		return res, err
	}
	if _, err := log.TrackOnce(eventlog.Event{
		Collection: CollectionBook, RecordID: sourceID, Action: ActionMerged,
		Version: version, Actor: actor, Data: data,
	}); err != nil {
		return res, fmt.Errorf("books: recording the merge of %s: %w", sourceID, err)
	}
	return res, nil
}
