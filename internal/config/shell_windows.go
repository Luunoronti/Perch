//go:build windows

package config

import "os"

func defaultShellPath() string {
	path := `C:\Program Files\PowerShell\7\pwsh.exe`
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return "pwsh.exe" // fall back to PATH lookup
}
