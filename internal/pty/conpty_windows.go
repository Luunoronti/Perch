//go:build windows

package pty

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/UserExistsError/conpty"
)

type conPTY struct {
	cpty *conpty.ConPty
}

// Start launches shellPath with args attached to a new ConPTY of the given
// size. The child inherits the server process's environment and window
// station/token — see spec §6.1 for why that matters.
func Start(shellPath string, args []string, cols, rows uint16) (PTY, error) {
	workDir, err := os.UserHomeDir()
	if err != nil {
		workDir = ""
	}

	cpty, err := conpty.Start(
		buildCommandLine(shellPath, args),
		conpty.ConPtyDimensions(int(cols), int(rows)),
		conpty.ConPtyWorkDir(workDir),
	)
	if err != nil {
		return nil, fmt.Errorf("pty: failed to start %q: %w", shellPath, err)
	}
	return &conPTY{cpty: cpty}, nil
}

func (p *conPTY) Read(b []byte) (int, error)  { return p.cpty.Read(b) }
func (p *conPTY) Write(b []byte) (int, error) { return p.cpty.Write(b) }

func (p *conPTY) Close() error {
	return p.cpty.Close()
}

func (p *conPTY) Resize(cols, rows uint16) error {
	return p.cpty.Resize(int(cols), int(rows))
}

func (p *conPTY) Wait() (uint32, error) {
	return p.cpty.Wait(context.Background())
}

// buildCommandLine quotes shellPath and appends args as a single Windows
// command line string, as required by conpty.Start / CreateProcess.
func buildCommandLine(shellPath string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(shellPath))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
