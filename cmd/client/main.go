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
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"perch/internal/config"
	"perch/internal/proto"
)

func main() {
	configPath := flag.String("config", "", "path to client config JSON (default: <config dir>/client.json)")
	serverAddr := flag.String("server", "", "override server address:port")
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
	if *serverAddr != "" {
		cfg.Server = *serverAddr
	}

	code, err := run(cfg.Server)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perch:", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

func run(serverAddr string) (exitCode uint32, err error) {
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return 1, fmt.Errorf("connect to %s: %w", serverAddr, err)
	}
	defer conn.Close()

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

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

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

	// SIGWINCH -> RESIZE
	go func() {
		for range winch {
			cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				continue
			}
			_ = proto.WriteFrame(conn, proto.Frame{Type: proto.FrameResize, Payload: proto.EncodeResize(uint16(cols), uint16(rows))})
		}
	}()

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
