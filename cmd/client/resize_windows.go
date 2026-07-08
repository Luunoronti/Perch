//go:build windows

package main

import "time"

// watchResize reports terminal-size changes by polling: Windows consoles
// have no SIGWINCH equivalent to subscribe to.
func watchResize(getSize func() (cols, rows int, err error), onChange func(cols, rows uint16)) (stop func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		lastCols, lastRows := -1, -1
		for {
			select {
			case <-ticker.C:
				c, r, err := getSize()
				if err != nil || (c == lastCols && r == lastRows) {
					continue
				}
				lastCols, lastRows = c, r
				onChange(uint16(c), uint16(r))
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
