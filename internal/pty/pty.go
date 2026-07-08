// Package pty wraps ConPTY so the rest of the server never touches the
// Windows API directly. See remote-pwsh-terminal-spec.md §6.
package pty

import "io"

// PTY is a running pseudo-console with an attached child process.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Wait() (exitCode uint32, err error)
}
