package eventlog_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
)

// Every storage adapter answers to the same suite. The in-memory one is what the rest of the
// tests use; the JSONL one is what actually holds the books.
func eachStorage(t *testing.T, fn func(t *testing.T, s eventlog.Storage)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		fn(t, eventlog.NewMemory())
	})

	t.Run("jsonl", func(t *testing.T) {
		s, err := eventlog.OpenJSONL(filepath.Join(t.TempDir(), "log.jsonl"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func fact(recordID, action string) eventlog.Event {
	return eventlog.Event{
		Collection: "transaction",
		RecordID:   recordID,
		Action:     action,
		Version:    1,
		Actor:      "human",
	}
}

func TestTrackStampsEachEventAndReadsThemBackInOrder(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		for _, action := range []string{"imported", "categorized", "matched"} {
			e, err := log.Track(fact("abc", action))
			if err != nil {
				t.Fatalf("track %s: %v", action, err)
			}
			if e.ID == "" {
				t.Errorf("track %s: no id stamped", action)
			}
			if e.Time.IsZero() {
				t.Errorf("track %s: no time stamped", action)
			}
		}

		events, err := log.All()
		if err != nil {
			t.Fatalf("all: %v", err)
		}
		var got []string
		for _, e := range events {
			got = append(got, e.Action)
		}
		want := []string{"imported", "categorized", "matched"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})
}

// A statement line is imported exactly once, however many overlapping statements report it.
func TestTrackOnceRefusesASecondEventForTheSameRecordAndAction(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		if _, err := log.TrackOnce(fact("abc", "imported")); err != nil {
			t.Fatalf("first import: %v", err)
		}
		if _, err := log.TrackOnce(fact("abc", "imported")); !errors.Is(err, eventlog.ErrAlreadyTracked) {
			t.Fatalf("second import: got %v, want ErrAlreadyTracked", err)
		}

		events, _ := log.All()
		if len(events) != 1 {
			t.Fatalf("got %d events, want 1", len(events))
		}
	})
}

func TestTrackOnceScopesUniquenessToTheAction(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		if _, err := log.TrackOnce(fact("abc", "imported")); err != nil {
			t.Fatalf("import: %v", err)
		}
		if _, err := log.TrackOnce(fact("abc", "matched")); err != nil {
			t.Fatalf("match: %v", err)
		}

		events, _ := log.All()
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2", len(events))
		}
	})
}

// Asserting a line's postings twice is two facts, not an overwrite. The later fold wins, and the
// history of every correction survives.
func TestTrackAllowsRepeatedEventsForTheSameRecordAndAction(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		for range 2 {
			if _, err := log.Track(fact("abc", "categorized")); err != nil {
				t.Fatalf("categorize: %v", err)
			}
		}

		events, _ := log.All()
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2", len(events))
		}
	})
}

func TestTrackRejectsAnIncompleteEvent(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		cases := map[string]func(*eventlog.Event){
			"no collection": func(e *eventlog.Event) { e.Collection = "" },
			"no record id":  func(e *eventlog.Event) { e.RecordID = "" },
			"no action":     func(e *eventlog.Event) { e.Action = "" },
			"no actor":      func(e *eventlog.Event) { e.Actor = "" },
			"no version":    func(e *eventlog.Event) { e.Version = 0 },
			"broken data":   func(e *eventlog.Event) { e.Data = json.RawMessage("{oops") },
		}
		for name, break_ := range cases {
			e := fact("abc", "imported")
			break_(&e)
			if _, err := log.Track(e); err == nil {
				t.Errorf("%s: tracked anyway", name)
			}
		}

		events, _ := log.All()
		if len(events) != 0 {
			t.Fatalf("got %d events, want 0", len(events))
		}
	})
}

// Money is integer cents. Data is raw JSON precisely so nothing round-trips through float64 on
// the way to a ledger.
func TestDataRoundTripsWithoutPassingThroughAFloat(t *testing.T) {
	type posting struct {
		Account     string `json:"account"`
		AmountCents int64  `json:"amount_cents"`
	}

	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		want := []posting{{"Expenses:Materials:Unit 1", 4000}, {"Expenses:Materials:Unit 2", 4420}}
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		e := fact("abc", "categorized")
		e.Data = data
		if _, err := log.Track(e); err != nil {
			t.Fatalf("track: %v", err)
		}

		events, _ := log.All()
		if string(events[0].Data) != string(data) {
			t.Fatalf("data changed on the way through storage: %s", events[0].Data)
		}

		var got []posting
		if err := events[0].Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got) != 2 || got[0].AmountCents != 4000 || got[1].AmountCents != 4420 {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestEventWithoutDataIsStillValidJSON(t *testing.T) {
	eachStorage(t, func(t *testing.T, s eventlog.Storage) {
		log := eventlog.New(s)

		if _, err := log.Track(fact("abc", "matched")); err != nil {
			t.Fatalf("track: %v", err)
		}

		events, _ := log.All()
		if !json.Valid(events[0].Data) {
			t.Fatalf("data is not valid json: %q", events[0].Data)
		}
	})
}

// A destination sends this as its Idempotency-Key, so it has to be derivable from the stored
// event and identical every time a retry regenerates it.
func TestKeyIsStableAndIdentifiesTheEvent(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())

	a, err := log.Track(fact("abc", "imported"))
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	b, err := log.Track(fact("abc", "imported"))
	if err != nil {
		t.Fatalf("track: %v", err)
	}

	if a.Key() != a.Key() {
		t.Error("key is not stable across calls")
	}
	if a.Key() == b.Key() {
		t.Error("two events share a key")
	}
	for _, part := range []string{a.Collection, a.RecordID, a.Action, a.ID} {
		if !strings.Contains(a.Key(), part) {
			t.Errorf("key %q omits %q", a.Key(), part)
		}
	}
}
