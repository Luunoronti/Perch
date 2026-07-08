// perch: terminal client for perch-server. Behaves like ssh — connects and
// gives you an interactive pwsh shell in your terminal window.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"perch/internal/config"
	"perch/internal/proto"
)

func main() {
	configPath := flag.String("config", "", "path to client config JSON (default: <config dir>/client.json)")
	serverAddr := flag.String("server", "", "override server address:port for this run")
	setDefaultServer := flag.String("default-server", "", "save address:port as the default server in config and exit")
	sessionName := flag.String("session", "", "persistent session name; omit for a one-shot session that dies on disconnect")
	listSessions := flag.Bool("list-sessions", false, "list persistent sessions running on the server and exit")
	flag.Parse()

	path := *configPath
	if path == "" {
		dir, err := config.Dir()
		if err != nil {
			log.Fatalf("config dir: %v", err)
		}
		path = filepath.Join(dir, "client.json")
	}

	cfg, err := config.LoadClientConfig(path)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if *setDefaultServer != "" {
		cfg.Server = *setDefaultServer
		if err := config.SaveClientConfig(path, cfg); err != nil {
			log.Fatalf("save config: %v", err)
		}
		fmt.Printf("perch: default server set to %s in %s\n", cfg.Server, path)
		os.Exit(0)
	}

	if *serverAddr != "" {
		cfg.Server = *serverAddr
	}

	if *listSessions {
		if err := runListSessions(cfg.Server); err != nil {
			fmt.Fprintln(os.Stderr, "perch:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	code, err := run(cfg.Server, *sessionName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perch:", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

// runListSessions is a one-shot request/response exchange -- no raw mode,
// no shell attach. See proto.FrameListSessions.
func runListSessions(serverAddr string) error {
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", serverAddr, err)
	}
	defer conn.Close()

	if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameListSessions}); err != nil {
		return fmt.Errorf("send LIST_SESSIONS: %w", err)
	}

	frame, err := proto.ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if frame.Type != proto.FrameSessionList {
		return fmt.Errorf("unexpected response frame type %v", frame.Type)
	}
	fmt.Print(string(frame.Payload))
	return nil
}

func run(serverAddr, sessionName string) (exitCode uint32, err error) {
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return 1, fmt.Errorf("connect to %s: %w", serverAddr, err)
	}
	defer conn.Close()

	if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameSession, Payload: []byte(sessionName)}); err != nil {
		return 1, fmt.Errorf("send SESSION: %w", err)
	}

	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		cols, rows = 80, 24
	}
	if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameResize, Payload: proto.EncodeResize(uint16(cols), uint16(rows))}); err != nil {
		return 1, fmt.Errorf("send initial RESIZE: %w", err)
	}

	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return 1, fmt.Errorf("enter raw mode: %w", err)
	}
	restored := false
	restore := func() {
		if !restored {
			term.Restore(stdinFd, oldState)
			restored = true
		}
	}
	defer restore()

	exitCh := make(chan uint32, 1)
	errCh := make(chan error, 2)

	// os.Stdin -> DATA -> conn
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameData, Payload: buf[:n]}); werr != nil {
					errCh <- werr
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// conn -> DATA -> os.Stdout ; EXIT -> exitCh
	go func() {
		for {
			frame, err := proto.ReadFrame(conn)
			if err != nil {
				errCh <- err
				return
			}
			switch frame.Type {
			case proto.FrameData:
				if _, werr := os.Stdout.Write(frame.Payload); werr != nil {
					errCh <- werr
					return
				}
			case proto.FrameExit:
				code, err := proto.DecodeExit(frame.Payload)
				if err != nil {
					errCh <- err
					return
				}
				exitCh <- code
				return
			case proto.FramePong:
				// keepalive ack, nothing to do
			}
		}
	}()

	// Terminal resize -> RESIZE frame (SIGWINCH on Unix, polling on Windows;
	// see resize_unix.go / resize_windows.go).
	stopResize := watchResize(
		func() (int, int, error) { return term.GetSize(int(os.Stdout.Fd())) },
		func(cols, rows uint16) {
			_ = proto.WriteFrame(conn, proto.Frame{Type: proto.FrameResize, Payload: proto.EncodeResize(cols, rows)})
		},
	)
	defer stopResize()

	select {
	case code := <-exitCh:
		restore()
		return code, nil
	case err := <-errCh:
		restore()
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		return 1, err
	}
}
