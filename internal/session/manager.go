package session

import (
	"sort"
	"sync"
)

// Info is a snapshot of a persistent session for listing purposes.
type Info struct {
	Name    string
	Clients int
}

// Manager tracks named persistent sessions. Ephemeral sessions (name =="")
// are never registered here -- each connection gets a brand new one.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// Attach returns the named persistent session if one is already running,
// or starts a new session (persistent if name != "", ephemeral otherwise).
// existed reports whether an already-running session was reused.
func (m *Manager) Attach(name, shell string, args []string, cols, rows uint16) (s *Session, existed bool, err error) {
	if name == "" {
		s, err = newSession("", shell, args, cols, rows)
		return s, false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.sessions[name]; ok {
		select {
		case <-existing.Done():
			delete(m.sessions, name) // stale entry; fall through and recreate
		default:
			return existing, true, nil
		}
	}

	s, err = newSession(name, shell, args, cols, rows)
	if err != nil {
		return nil, false, err
	}
	m.sessions[name] = s
	go func() {
		<-s.Done()
		m.mu.Lock()
		if m.sessions[name] == s {
			delete(m.sessions, name)
		}
		m.mu.Unlock()
	}()
	return s, false, nil
}

// List returns a snapshot of all currently running persistent sessions,
// sorted by name. Ephemeral sessions are never included -- they aren't
// tracked here.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.sessions))
	for name, s := range m.sessions {
		out = append(out, Info{Name: name, Clients: s.ClientCount()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
