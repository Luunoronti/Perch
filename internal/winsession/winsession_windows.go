//go:build windows

package winsession

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func isInteractive() (bool, error) {
	activeSession := windows.WTSGetActiveConsoleSessionId()
	if activeSession == 0xFFFFFFFF {
		return false, fmt.Errorf("winsession: no active console session")
	}

	var ownSession uint32
	pid := windows.GetCurrentProcessId()
	if err := windows.ProcessIdToSessionId(pid, &ownSession); err != nil {
		return false, fmt.Errorf("winsession: ProcessIdToSessionId: %w", err)
	}

	return ownSession == activeSession, nil
}
