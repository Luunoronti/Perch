// Package session multiplexes a single ConPTY-backed shell across one or
// more client connections, and optionally keeps it alive across
// disconnects (persistent sessions) so a client can reattach later, even
// from a different machine.
package session

import (
	"sync"

	"perch/internal/pty"
)

// maxBacklog caps how many trailing bytes of a session's output are kept
// for replay to newly (re)attaching clients -- see Subscribe.
const maxBacklog = 256 * 1024

// termSize is one client's last-known terminal dimensions.
type termSize struct {
	cols, rows uint16
}

// Session wraps one running shell. If Name is empty it is ephemeral: the
// caller (server) is expected to kill it when its single client
// disconnects. If Name is non-empty it is persistent and stays alive,
// managed by a Manager, until the shell process itself exits.
type Session struct {
	Name string

	pty pty.PTY

	mu          sync.Mutex
	clients     map[chan []byte]struct{}
	clientSizes map[chan []byte]termSize // tmux-style: pty is sized to the smallest attached client
	curSize     termSize                 // last size actually applied to the pty
	backlog     []byte                   // trailing output, replayed to new subscribers
	closeOnce   sync.Once
	closed      chan struct{}
	exitCode    uint32
}

func newSession(name string, shell string, args []string, cols, rows uint16) (*Session, error) {
	p, err := pty.Start(shell, args, cols, rows)
	if err != nil {
		return nil, err
	}
	s := &Session{
		Name:        name,
		pty:         p,
		clients:     make(map[chan []byte]struct{}),
		clientSizes: make(map[chan []byte]termSize),
		curSize:     termSize{cols, rows},
		closed:      make(chan struct{}),
	}
	go s.pump()
	go s.reap()
	return s, nil
}

// pump copies ConPTY output to every currently subscribed client. It
// exits once the pty is closed (either the process exited and reap()
// closed it, or Terminate() force-closed it).
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			s.broadcast(payload)
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the shell process to exit -- the only reliable exit
// signal, since ConPTY's Read() can block forever after the child exits
// (see remote-pwsh-terminal-spec.md notes on conhost keeping the pipe
// open) -- and then force-closes the pty to unblock pump().
func (s *Session) reap() {
	code, _ := s.pty.Wait()
	s.mu.Lock()
	s.exitCode = code
	s.mu.Unlock()
	s.closePty()
	close(s.closed)
}

// Terminate force-kills the session's shell. Used for ephemeral sessions
// when their one and only client disconnects.
func (s *Session) Terminate() {
	s.closePty()
}

func (s *Session) closePty() {
	s.closeOnce.Do(func() { s.pty.Close() })
}

// Done is closed once the shell process has exited (naturally or via
// Terminate).
func (s *Session) Done() <-chan struct{} {
	return s.closed
}

// ExitCode is only meaningful after Done() is closed.
func (s *Session) ExitCode() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// ClientCount returns how many clients are currently subscribed.
func (s *Session) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *Session) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

// Resize updates ch's (the client identified by its Subscribe channel)
// known terminal size and re-fits the pty to the smallest currently
// attached client -- see recomputeSize.
func (s *Session) Resize(ch chan []byte, cols, rows uint16) error {
	s.mu.Lock()
	s.clientSizes[ch] = termSize{cols, rows}
	s.mu.Unlock()
	return s.recomputeSize()
}

// Subscribe registers a new output listener with its initial terminal
// size and returns it along with the session's current backlog (the
// trailing output produced so far), so a (re)attaching client can be
// caught up on what it missed instead of facing a blank terminal. The
// backlog is captured atomically with registration -- no output can be
// produced between the two -- so nothing is duplicated or dropped in the
// handoff to live broadcasts on ch.
//
// The caller must Unsubscribe when done to avoid leaking the channel and
// its goroutine.
func (s *Session) Subscribe(cols, rows uint16) (ch chan []byte, backlog []byte) {
	ch = make(chan []byte, 256)
	s.mu.Lock()
	backlog = append([]byte(nil), s.backlog...)
	s.clients[ch] = struct{}{}
	s.clientSizes[ch] = termSize{cols, rows}
	s.mu.Unlock()
	s.recomputeSize()
	return ch, backlog
}

// Unsubscribe removes and closes a listener registered with Subscribe,
// then re-fits the pty in case the departing client was the one
// constraining the size to something smaller than the rest.
func (s *Session) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	if _, ok := s.clients[ch]; ok {
		delete(s.clients, ch)
		delete(s.clientSizes, ch)
		close(ch)
	}
	s.mu.Unlock()
	s.recomputeSize()
}

// recomputeSize re-fits the pty to the smallest cols and smallest rows
// among all currently attached clients (tmux's default behavior for a
// session with multiple attached clients of different window sizes): no
// client ever gets a viewport bigger than its own actual terminal.
func (s *Session) recomputeSize() error {
	s.mu.Lock()
	var min termSize
	first := true
	for _, sz := range s.clientSizes {
		if first || sz.cols < min.cols {
			min.cols = sz.cols
		}
		if first || sz.rows < min.rows {
			min.rows = sz.rows
		}
		first = false
	}
	if first || min == s.curSize {
		s.mu.Unlock()
		return nil
	}
	s.curSize = min
	s.mu.Unlock()
	return s.pty.Resize(min.cols, min.rows)
}

func (s *Session) broadcast(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.backlog = append(s.backlog, b...)
	if len(s.backlog) > maxBacklog {
		// Copy rather than reslice: a long-lived persistent session must
		// not keep pinning an ever-growing backing array via a small
		// trailing slice of it.
		trimmed := make([]byte, maxBacklog)
		copy(trimmed, s.backlog[len(s.backlog)-maxBacklog:])
		s.backlog = trimmed
	}

	for ch := range s.clients {
		select {
		case ch <- b:
		default:
			// Slow client, drop rather than stall the whole session.
		}
	}
}
