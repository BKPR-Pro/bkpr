package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dallasread/bookkeeper/store"
)

func TestInitCreatesABookOfRecord(t *testing.T) {
	dir := t.TempDir()

	path, err := store.Init(dir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("the marker should be a directory")
	}
	// The log carries whatever the bank put in a statement.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 700", perm)
	}
}

// Everything in the store is committed, so init writes no .gitignore. There is nothing derived
// that is not also worth reading: the log is text, and the ledger is its artifact.
func TestInitLeavesNothingToIgnore(t *testing.T) {
	dir := t.TempDir()
	path, err := store.Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(path, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("init wrote a .gitignore; nothing in the store is meant to be ignored")
	}
}

func TestInitRefusesToOverwriteAnExistingBookOfRecord(t *testing.T) {
	dir := t.TempDir()
	if _, err := store.Init(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Init(dir); err == nil {
		t.Fatal("initialized over an existing book of record")
	}
}

// Run bookkeeper from anywhere under your books, as you would run git.
func TestFindWalksUpFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	want, err := store.Init(root)
	if err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(root, "statements", "2026", "march")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := store.Find(deep)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != want {
		t.Errorf("found %q, want %q", got, want)
	}
}

func TestFindStopsAtTheFilesystemRoot(t *testing.T) {
	if _, err := store.Find(t.TempDir()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// The bug this package exists to close. A wrong working directory used to create a new, empty book
// of record; folding it produced an empty ledger, and a shell redirect wrote that over the real
// one. Exit status zero, no warning.
func TestOpenNeverCreatesABookOfRecord(t *testing.T) {
	dir := t.TempDir()

	if _, err := store.Open(dir); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("opening created %v", entries)
	}
}

func TestOpenFindsTheLogAndTheFilesBesideIt(t *testing.T) {
	dir := t.TempDir()
	path, err := store.Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	events, err := s.Log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("a new book of record has %d events, want 0", len(events))
	}
	if got, want := s.Ledger(), filepath.Join(path, store.LedgerFile); got != want {
		t.Errorf("ledger path = %q, want %q", got, want)
	}
}
