package eventlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// JSONL holds the log as one JSON object per line. The file is the book of record: append-only,
// human-readable, and diffable, so committing it to git gives a backup, an audit trail, and a
// check on the one invariant the whole design rests on. Any git diff that is not a pure append
// means something rewrote history.
//
// It keeps the whole log in memory. It has to fold the file on open to answer All and to know
// which once-only facts are already present, and at the volume a set of books reaches (a few
// hundred lines a year) the log is small. The file on disk is the truth; the slice is a mirror the
// one writer keeps in step.
type JSONL struct {
	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	events []Event
	once   map[string]struct{}
}

// OpenJSONL opens or creates the log at path, taking an exclusive lock so a second writer cannot
// append a fact this one has not seen and so break the uniqueness AppendOnce enforces in memory.
func OpenJSONL(path string) (*JSONL, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	if err := lock(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("eventlog: %s is already open for writing: %w", path, err)
	}

	j := &JSONL{file: file, once: make(map[string]struct{})}
	if err := j.load(); err != nil {
		file.Close()
		return nil, err
	}
	j.writer = bufio.NewWriter(file)
	return j, nil
}

// load folds the file into memory. A crash mid-append can leave a torn final line; since the log
// only ever grows, only the last line can be partial, so a trailing line that will not parse is
// dropped and the file truncated back to the last whole event. An unparseable line anywhere else
// means the history was corrupted or edited, which is refused rather than guessed at.
func (j *JSONL) load() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	reader := bufio.NewReader(j.file)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		atEOF := errors.Is(err, io.EOF)
		if err != nil && !atEOF {
			return fmt.Errorf("eventlog: read: %w", err)
		}

		// A final chunk with no newline is a torn write. Drop it and heal the file.
		if atEOF && len(line) > 0 {
			if terr := j.file.Truncate(offset); terr != nil {
				return fmt.Errorf("eventlog: healing a torn final line: %w", terr)
			}
			break
		}
		if atEOF {
			break
		}

		var e Event
		if uerr := json.Unmarshal(line, &e); uerr != nil {
			return fmt.Errorf("eventlog: %s is corrupt at byte %d: %w", j.file.Name(), offset, uerr)
		}
		j.events = append(j.events, e)
		j.once[onceKey(e)] = struct{}{}
		offset += int64(len(line))
	}

	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return nil
}

func (j *JSONL) Append(e Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.append(e)
}

func (j *JSONL) AppendOnce(e Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, seen := j.once[onceKey(e)]; seen {
		return ErrAlreadyTracked
	}
	return j.append(e)
}

// append writes one event as a line and flushes it to disk before returning, so a fact the caller
// was told is recorded survives a crash. The in-memory mirror is only updated once the bytes are
// down, so a failed write leaves memory and file agreeing.
func (j *JSONL) append(e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.writer.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := j.writer.Flush(); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}

	j.events = append(j.events, e)
	j.once[onceKey(e)] = struct{}{}
	return nil
}

func (j *JSONL) All() ([]Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Event, len(j.events))
	copy(out, j.events)
	return out, nil
}

func (j *JSONL) Close() error { return j.file.Close() }

// ReadEvents parses a log read from elsewhere — another book's log.jsonl — into its events. It
// reads without locking or healing: the file belongs to another book, so a torn or corrupt line
// is refused rather than repaired in place.
func ReadEvents(r io.Reader) ([]Event, error) {
	var events []Event
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("eventlog: line %d is not an event: %w", n, err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: read: %w", err)
	}
	return events, nil
}
