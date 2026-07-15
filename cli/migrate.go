package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// migrateCmd restates every imported line's fingerprint under the door it entered through: the
// connector's name, or the statement file's own name. A book recorded before the door-scoped
// identity scheme keyed its lines to the account they landed in, so those ids no longer match what
// an import computes and any overlapping window re-lands its history as duplicates. Restating them
// once re-arms the dedupe; every fact keyed to an old id follows it, so the books fold identically
// before and after.
//
// This rewrites the log in place -- the one deliberate exception, with reset, to append-only -- so
// without -confirm it is a dry run, and the log should be committed first: git is the undo.
func migrateCmd(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "actually rewrite the log; without it, a dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	logPath := filepath.Join(s.Path, store.LogFile)
	events, err := s.Log.All()
	if err != nil {
		s.Close()
		return err
	}

	restated, moved, repointed, err := restateDoors(events)
	if err != nil {
		s.Close()
		return err
	}
	if moved == 0 {
		s.Close()
		fmt.Println("every line is already keyed to its door; nothing to migrate")
		return nil
	}
	if !*confirm {
		s.Close()
		fmt.Printf("would restate %d imported line id(s) under their doors and re-point %d later reference(s) (dry run; add -confirm to migrate)\n", moved, repointed)
		fmt.Printf("migrate rewrites %s in place; commit it first so git holds the old ids\n", logPath)
		return nil
	}

	// As with reset, the lock is released before the file is replaced, so the rewrite is not
	// fighting the mirror an open log keeps in memory.
	if err := s.Close(); err != nil {
		return err
	}
	if err := rewriteLog(logPath, restated); err != nil {
		return err
	}
	fmt.Printf("migrated: %d imported line id(s) restated under their doors, %d later reference(s) re-pointed\n", moved, repointed)
	return nil
}

// restateDoors maps each imported line's id to the door-scoped fingerprint the current binary
// would compute, numbering identical lines -N in log order exactly as one import of them all
// would, and re-points every fact keyed to an old id: the record ids of later transaction events,
// a match's partner, a settlement's paying line. Event order, event ids, times, and all other
// data are preserved, so the books fold identically; only the names of the line ids change.
func restateDoors(events []eventlog.Event) ([]eventlog.Event, int, int, error) {
	ids := map[string]string{}
	seen := map[string]int{}
	moved := 0
	for _, e := range events {
		if e.Collection != books.CollectionTransaction || e.Action != books.ActionImported {
			continue
		}
		var d struct {
			Date        time.Time    `json:"date"`
			Amount      model.Amount `json:"amount"`
			Description string       `json:"description"`
		}
		if err := e.Decode(&d); err != nil {
			return nil, 0, 0, fmt.Errorf("migrate: line %s: %w", e.RecordID, err)
		}
		door, ok := doorOf(e.Actor)
		if !ok {
			return nil, 0, 0, fmt.Errorf("migrate: line %s was imported by %q, which names no door; refusing to guess its scope", e.RecordID, e.Actor)
		}
		fp := source.Fingerprint(door, d.Date, d.Amount, d.Description)
		seen[fp]++
		id := fmt.Sprintf("%s-%d", fp, seen[fp])
		ids[e.RecordID] = id
		if id != e.RecordID {
			moved++
		}
	}

	repointed := 0
	out := make([]eventlog.Event, len(events))
	for i, e := range events {
		out[i] = e
		if e.Collection == books.CollectionTransaction {
			id, ok := ids[e.RecordID]
			if !ok {
				return nil, 0, 0, fmt.Errorf("migrate: a %s event is keyed to line %s, which no import recorded", e.Action, e.RecordID)
			}
			if id != e.RecordID {
				out[i].RecordID = id
				if e.Action != books.ActionImported {
					repointed++
				}
			}
			if e.Action == books.ActionMatched {
				changed, err := repoint(&out[i], "with", ids)
				if err != nil {
					return nil, 0, 0, fmt.Errorf("migrate: match on %s: %w", e.RecordID, err)
				}
				if changed {
					repointed++
				}
			}
		}
		if (e.Collection == books.CollectionInvoice || e.Collection == books.CollectionBill) && e.Action == books.ActionSettled {
			changed, err := repoint(&out[i], "tx", ids)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("migrate: settlement of %s: %w", e.RecordID, err)
			}
			if changed {
				repointed++
			}
		}
	}
	return out, moved, repointed, nil
}

// doorOf names the fingerprint scope an actor's lines entered under -- the connector's name or the
// statement file's own name, exactly what identified them at import time.
func doorOf(actor string) (string, bool) {
	if door, ok := strings.CutPrefix(actor, "connector:"); ok {
		return door, true
	}
	if door, ok := strings.CutPrefix(actor, "statement:"); ok {
		return door, true
	}
	return "", false
}

// repoint rewrites one line-id-valued field of an event's data through ids. An event whose field
// is absent, empty, or already current keeps its data byte for byte.
func repoint(e *eventlog.Event, field string, ids map[string]string) (bool, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return false, err
	}
	var old string
	if raw, ok := data[field]; !ok {
		return false, nil
	} else if err := json.Unmarshal(raw, &old); err != nil {
		return false, err
	}
	if old == "" {
		return false, nil
	}
	id, ok := ids[old]
	if !ok {
		return false, fmt.Errorf("its %s names line %s, which no import recorded", field, old)
	}
	if id == old {
		return false, nil
	}
	value, err := json.Marshal(id)
	if err != nil {
		return false, err
	}
	data[field] = value
	rewritten, err := json.Marshal(data)
	if err != nil {
		return false, err
	}
	e.Data = rewritten
	return true, nil
}

// rewriteLog replaces the log by writing a sibling file and renaming it into place, so a failure
// partway leaves the original untouched.
func rewriteLog(path string, events []eventlog.Event) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), store.LogFile+".migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename lands
	w := bufio.NewWriter(tmp)
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
