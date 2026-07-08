//go:build !windows

package config

func defaultShellPath() string {
	return "pwsh"
}
