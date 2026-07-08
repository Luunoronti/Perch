//go:build !windows

package winsession

func isInteractive() (bool, error) {
	return true, nil
}
