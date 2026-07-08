//go:build !windows

package pty

import "errors"

// Start is unavailable outside Windows; this stub exists only so the
// module compiles for static analysis / IDE tooling on non-Windows hosts.
// perch-server is Windows-only, see spec §1.
func Start(shellPath string, args []string, cols, rows uint16) (PTY, error) {
	return nil, errors.New("pty: ConPTY is only available on windows")
}
