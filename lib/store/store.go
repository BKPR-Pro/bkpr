// Package store locates the book of record.
//
// A directory holds a set of books the way it holds a git repository: a `.bkpr` marker at
// its root, found by walking up from wherever you happen to be standing.
//
// Everything inside it is committed. The log is the truth, one JSON object per line, so its git
// diff is an append and any diff that is not an append means history was rewritten. The ledger is
// its artifact, kept because its diff is the readable account of what changed. There is nothing to
// ignore, because there is nothing derived that is not also worth reading.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dallasread/bkpr/lib/eventlog"
)

const (
	// Dir marks a directory as holding a set of books.
	Dir = ".bkpr"

	// LogFile is the book of record: append-only, one JSON object per line.
	LogFile = "log.jsonl"

	// LedgerFile is the generated artifact: read-only output, regenerated from the log.
	LedgerFile = "books.ledger"
)

// ErrNotFound reports that no book of record was found. It is never resolved by creating one.
var ErrNotFound = errors.New("no .bkpr here or in any parent directory; run `bkpr init`")

// Store is an open book of record.
type Store struct {
	Path string // the .bkpr directory
	Log  *eventlog.Log

	close func() error
}

func (s *Store) Close() error { return s.close() }

// Ledger is the path of the generated artifact, whether or not it exists yet.
func (s *Store) Ledger() string { return filepath.Join(s.Path, LedgerFile) }

// Init creates a book of record in dir.
func Init(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, Dir)

	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("store: %s already holds a book of record", path)
	}
	// The log carries whatever the bank put in a statement, so it is nobody else's business.
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// Find walks up from dir to the nearest book of record, as git does for a repository.
func Find(dir string) (string, error) {
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		path := filepath.Join(at, Dir)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path, nil
		}
		parent := filepath.Dir(at)
		if parent == at {
			return "", ErrNotFound
		}
		at = parent
	}
}

// Open finds the book of record and opens its log.
//
// It never creates one. A mistyped path or a wrong working directory must not quietly become a new
// and empty book of record, whose fold is an empty ledger, which a shell redirect then writes over
// a year of real books.
func Open(dir string) (*Store, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}

	jsonl, err := eventlog.OpenJSONL(filepath.Join(path, LogFile))
	if err != nil {
		return nil, err
	}
	return &Store{Path: path, Log: eventlog.New(jsonl), close: jsonl.Close}, nil
}

// OpenReader finds the book of record and opens its log for reading only.
//
// A read command is a fold over the log, not a write to it, so it takes no lock. That is what lets a
// query run while an import holds the log open — the exclusive lock Open takes is only there to keep
// a single writer, and enforcing it on reads locked queries out of their own books for no gain. The
// reader can never write, so it cannot break the one-writer guarantee it declines to hold.
func OpenReader(dir string) (*Store, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}

	jsonl, err := eventlog.OpenJSONLReader(filepath.Join(path, LogFile))
	if err != nil {
		return nil, err
	}
	return &Store{Path: path, Log: eventlog.New(jsonl), close: jsonl.Close}, nil
}
