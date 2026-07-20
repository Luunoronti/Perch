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

// Size is a terminal's dimensions in character cells.
type Size struct {
	Cols, Rows uint16
}

// Client is one attached listener. data carries live/backlog output;
// resized carries the pty size actually applied to the session (which may
// be smaller than this client's own terminal, since the pty is fitted to
// the smallest attached client -- see recomputeSize). The client uses it to
// wipe the margin of its terminal that the pty never draws into. resized is
// buffered and coalesced: only the latest applied size matters.
type Client struct {
	data    chan []byte
	resized chan Size
}

// Data is the channel of output payloads for this client; it is closed by
// Unsubscribe.
func (c *Client) Data() <-chan []byte { return c.data }

// Resized is the channel of applied session sizes for this client.
func (c *Client) Resized() <-chan Size { return c.resized }

// notifyResize delivers the latest applied size, dropping any unread stale
// value first so a slow reader always ends up with the current size rather
// than a queue of outdated ones.
func (c *Client) notifyResize(sz Size) {
	for {
		select {
		case c.resized <- sz:
			return
		default:
			select {
			case <-c.resized:
			default:
			}
		}
	}
}

// Session wraps one running shell. If Name is empty it is ephemeral: the
// caller (server) is expected to kill it when its single client
// disconnects. If Name is non-empty it is persistent and stays alive,
// managed by a Manager, until the shell process itself exits.
type Session struct {
	Name string

	pty pty.PTY

	mu          sync.Mutex
	clients     map[*Client]struct{}
	clientSizes map[*Client]Size // tmux-style: pty is sized to the smallest attached client
	curSize     Size             // last size actually applied to the pty
	backlog     []byte           // trailing output, replayed to new subscribers
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
		clients:     make(map[*Client]struct{}),
		clientSizes: make(map[*Client]Size),
		curSize:     Size{cols, rows},
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

// Resize updates c's known terminal size and re-fits the pty to the
// smallest currently attached client -- see recomputeSize.
func (s *Session) Resize(c *Client, cols, rows uint16) error {
	s.mu.Lock()
	s.clientSizes[c] = Size{cols, rows}
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
func (s *Session) Subscribe(cols, rows uint16) (c *Client, backlog []byte) {
	c = &Client{
		data:    make(chan []byte, 256),
		resized: make(chan Size, 1),
	}
	s.mu.Lock()
	backlog = append([]byte(nil), s.backlog...)
	s.clients[c] = struct{}{}
	s.clientSizes[c] = Size{cols, rows}
	s.mu.Unlock()
	s.recomputeSize()
	// Tell the new client the applied size even when it didn't change it
	// (e.g. a larger client joining an already-smaller session):
	// recomputeSize only notifies on a change, so seed this one explicitly
	// so it can wipe its margin right away.
	s.mu.Lock()
	applied := s.curSize
	s.mu.Unlock()
	c.notifyResize(applied)
	return c, backlog
}

// Unsubscribe removes and closes a listener registered with Subscribe,
// then re-fits the pty in case the departing client was the one
// constraining the size to something smaller than the rest.
func (s *Session) Unsubscribe(c *Client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		delete(s.clientSizes, c)
		close(c.data)
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
	var min Size
	first := true
	for _, sz := range s.clientSizes {
		if first || sz.Cols < min.Cols {
			min.Cols = sz.Cols
		}
		if first || sz.Rows < min.Rows {
			min.Rows = sz.Rows
		}
		first = false
	}
	if first || min == s.curSize {
		s.mu.Unlock()
		return nil
	}
	s.curSize = min
	// Broadcast the new applied size so every client can re-wipe the margin
	// its terminal has beyond the (now smaller/larger) session viewport.
	for c := range s.clients {
		c.notifyResize(min)
	}
	s.mu.Unlock()
	return s.pty.Resize(min.Cols, min.Rows)
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

	for c := range s.clients {
		select {
		case c.data <- b:
		default:
			// Slow client, drop rather than stall the whole session.
		}
	}
}
