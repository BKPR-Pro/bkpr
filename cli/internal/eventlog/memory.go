package eventlog

import "sync"

// Memory keeps the log in a slice. It is what the tests fold over, and what a dry run uses.
type Memory struct {
	mu     sync.Mutex
	events []Event
	once   map[string]struct{}
}

func NewMemory() *Memory {
	return &Memory{once: make(map[string]struct{})}
}

func (m *Memory) Append(e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *Memory) AppendOnce(e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := onceKey(e)
	if _, seen := m.once[k]; seen {
		return ErrAlreadyTracked
	}
	m.once[k] = struct{}{}
	m.events = append(m.events, e)
	return nil
}

func (m *Memory) All() ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out, nil
}
