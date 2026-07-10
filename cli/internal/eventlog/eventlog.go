// Package eventlog is the book of record: an append-only log of immutable, past-tense facts.
//
// Everything else in bookkeeper is a fold over it. The log holds only what cannot be recomputed:
// the lines a bank reported, the answers a model gave, and the judgments a person made. Rules are
// deterministic data kept in git, and a categorization is a pure function of a transaction and
// the rules, so neither is written here. That is what makes the books regenerable, and a diff in
// them meaningful.
//
// An event is never edited. A correction is a later event about the same record, so the history
// of how a line came to be categorized survives alongside its current state.
package eventlog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrAlreadyTracked reports that TrackOnce found the fact already recorded. It is the ordinary
// outcome of re-importing a statement, not a failure: the caller treats it as a no-op.
var ErrAlreadyTracked = errors.New("eventlog: already tracked")

// Event is one immutable fact. Its name says what happened; Actor says who. Reading Actor should
// never be necessary to know what kind of fact this is.
type Event struct {
	ID         string          `json:"id"`
	Time       time.Time       `json:"time"`
	Collection string          `json:"collection"`
	RecordID   string          `json:"record_id"`
	Action     string          `json:"action"`
	Version    int             `json:"version"`
	Actor      string          `json:"actor"`
	Data       json.RawMessage `json:"data"`
}

// Key identifies this event to anything downstream that must not act twice, and is what a
// destination sends as its Idempotency-Key. The event is durable before any side effect runs, so
// a retry rebuilds the same key from the same stored event.
func (e Event) Key() string {
	return strings.Join([]string{
		e.Time.UTC().Format(time.RFC3339Nano),
		e.Collection,
		e.RecordID,
		e.Action,
		"v" + strconv.Itoa(e.Version),
		e.ID,
	}, "/")
}

// Decode unmarshals the event's data into v.
func (e Event) Decode(v any) error {
	return json.Unmarshal(e.Data, v)
}

// Storage persists events in the order they were appended. AppendOnce returns ErrAlreadyTracked
// when an event with the same collection, record id and action is already present; Append imposes
// no such constraint. Which facts are once-only is the caller's business, not storage's.
type Storage interface {
	Append(Event) error
	AppendOnce(Event) error
	All() ([]Event, error)
}

// Log stamps facts and appends them. It is the only thing that writes to storage.
type Log struct {
	storage Storage
	now     func() time.Time
	newID   func() string
}

// Option configures a Log. The clock and the id source are seams so tests can pin both.
type Option func(*Log)

func WithClock(now func() time.Time) Option { return func(l *Log) { l.now = now } }
func WithIDs(newID func() string) Option    { return func(l *Log) { l.newID = newID } }

func New(s Storage, opts ...Option) *Log {
	l := &Log{storage: s, now: time.Now, newID: randomID}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Track appends a fact that may recur. Correcting the same line twice records two events.
func (l *Log) Track(e Event) (Event, error) { return l.track(e, l.storage.Append) }

// TrackOnce appends a fact that can only be true once, such as a statement line being imported.
// It returns ErrAlreadyTracked if the fact is already recorded.
func (l *Log) TrackOnce(e Event) (Event, error) { return l.track(e, l.storage.AppendOnce) }

func (l *Log) All() ([]Event, error) { return l.storage.All() }

func (l *Log) track(e Event, append func(Event) error) (Event, error) {
	if err := validate(e); err != nil {
		return Event{}, err
	}
	e.ID = l.newID()
	e.Time = l.now().UTC()
	if len(e.Data) == 0 {
		e.Data = json.RawMessage("null")
	}
	if err := append(e); err != nil {
		return Event{}, err
	}
	return e, nil
}

func validate(e Event) error {
	switch {
	case e.Collection == "":
		return errors.New("eventlog: event has no collection")
	case e.RecordID == "":
		return errors.New("eventlog: event has no record id")
	case e.Action == "":
		return errors.New("eventlog: event has no action")
	case e.Actor == "":
		return errors.New("eventlog: event has no actor")
	case e.Version < 1:
		return fmt.Errorf("eventlog: event version %d is below 1", e.Version)
	case len(e.Data) > 0 && !json.Valid(e.Data):
		return errors.New("eventlog: event data is not valid json")
	}
	return nil
}

// onceKey is the identity TrackOnce deduplicates on.
func onceKey(e Event) string {
	return strings.Join([]string{e.Collection, e.RecordID, e.Action}, "\x00")
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on any platform we run on
	}
	return hex.EncodeToString(b[:])
}
