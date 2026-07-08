// Package session multiplexes a single ConPTY-backed shell across one or
// more client connections, and optionally keeps it alive across
// disconnects (persistent sessions) so a client can reattach later, even
// from a different machine.
package session

import (
	"sync"

	"perch/internal/pty"
)

// Session wraps one running shell. If Name is empty it is ephemeral: the
// caller (server) is expected to kill it when its single client
// disconnects. If Name is non-empty it is persistent and stays alive,
// managed by a Manager, until the shell process itself exits.
type Session struct {
	Name string

	pty pty.PTY

	mu        sync.Mutex
	clients   map[chan []byte]struct{}
	closeOnce sync.Once
	closed    chan struct{}
	exitCode  uint32
}

func newSession(name string, shell string, args []string, cols, rows uint16) (*Session, error) {
	p, err := pty.Start(shell, args, cols, rows)
	if err != nil {
		return nil, err
	}
	s := &Session{
		Name:    name,
		pty:     p,
		clients: make(map[chan []byte]struct{}),
		closed:  make(chan struct{}),
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

func (s *Session) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

func (s *Session) Resize(cols, rows uint16) error {
	return s.pty.Resize(cols, rows)
}

// Subscribe registers a new output listener. The caller must Unsubscribe
// when done to avoid leaking the channel and its goroutine.
func (s *Session) Subscribe() chan []byte {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes a listener registered with Subscribe.
func (s *Session) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	if _, ok := s.clients[ch]; ok {
		delete(s.clients, ch)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Session) broadcast(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- b:
		default:
			// Slow client, drop rather than stall the whole session.
		}
	}
}
