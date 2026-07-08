// Package winsession provides the interactive-session sanity check
// described in remote-pwsh-terminal-spec.md §9: perch-server must run
// inside the logged-in user's desktop session, not as a Session 0 service.
package winsession

// IsInteractive reports whether the current process is running in the
// active console session. On non-Windows platforms it always returns true
// (the check is meaningless there).
func IsInteractive() (bool, error) {
	return isInteractive()
}
