//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize reports terminal-size changes via SIGWINCH, the standard
// Unix mechanism (see spec §7).
func watchResize(getSize func() (cols, rows int, err error), onChange func(cols, rows uint16)) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				if c, r, err := getSize(); err == nil {
					onChange(uint16(c), uint16(r))
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}
