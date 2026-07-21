//go:build !windows

package main

import "errors"

// runTray is only implemented on Windows; see tray_windows.go. The stub
// keeps main.go compiling on other platforms (the client cross-compiles for
// Linux, and `go build ./...` on any host should stay green).
func runTray(listenAddr string, serve func()) error {
	return errors.New("tray mode is only supported on Windows")
}

// attachConsole is a Windows-only concern (reattaching a GUI-subsystem binary
// to its parent console); a no-op elsewhere.
func attachConsole() {}

// trayHasConsole / relaunchDetached are Windows-only; the stubs keep main.go
// compiling on other platforms.
func trayHasConsole() bool    { return false }
func relaunchDetached() error { return errors.New("detach is only supported on Windows") }
