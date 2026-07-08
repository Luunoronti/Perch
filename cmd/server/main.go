// perch-server: remote pwsh terminal server. MUST run inside the logged-in
// user's interactive desktop session — see remote-pwsh-terminal-spec.md §9.
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"path/filepath"

	"perch/internal/config"
	"perch/internal/proto"
	"perch/internal/pty"
	"perch/internal/winsession"
)

func main() {
	configPath := flag.String("config", "", "path to server config JSON (default: <config dir>/server.json)")
	listenAddr := flag.String("listen", "", "override listen address:port")
	flag.Parse()

	if ok, err := winsession.IsInteractive(); err != nil {
		log.Printf("warning: could not determine session type: %v", err)
	} else if !ok {
		log.Println("WARNING: perch-server is not running in the active console session.")
		log.Println("WARNING: GUI popups and DPAPI will not work; run me from your logged-in desktop session, not as a service.")
	}

	path := *configPath
	if path == "" {
		dir, err := config.Dir()
		if err != nil {
			log.Fatalf("config dir: %v", err)
		}
		path = filepath.Join(dir, "server.json")
	}

	cfg, err := config.LoadServerConfig(path)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *listenAddr != "" {
		cfg.Listen = *listenAddr
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Listen, err)
	}
	log.Printf("perch-server listening on %s (plain TCP, no auth — see spec §8)", cfg.Listen)
	log.Printf("shell: %s %v", cfg.Shell, cfg.ShellArgs)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, cfg)
	}
}

func handleConn(conn net.Conn, cfg config.ServerConfig) {
	remote := conn.RemoteAddr()
	log.Printf("connection from %s", remote)
	defer func() {
		conn.Close()
		log.Printf("connection from %s closed", remote)
	}()

	// First frame must be RESIZE with the initial terminal size (spec §4.3).
	frame, err := proto.ReadFrame(conn)
	if err != nil {
		log.Printf("%s: read initial frame: %v", remote, err)
		return
	}
	if frame.Type != proto.FrameResize {
		log.Printf("%s: expected RESIZE as first frame, got %v", remote, frame.Type)
		return
	}
	cols, rows, err := proto.DecodeResize(frame.Payload)
	if err != nil {
		log.Printf("%s: %v", remote, err)
		return
	}

	p, err := pty.Start(cfg.Shell, cfg.ShellArgs, cols, rows)
	if err != nil {
		log.Printf("%s: start pty: %v", remote, err)
		return
	}

	errCh := make(chan error, 2)
	exitCh := make(chan uint32, 1)

	// ConPTY -> DATA frames -> conn
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := p.Read(buf)
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

	// conn frames -> DATA -> ConPTY.Write ; RESIZE -> ConPTY.Resize
	go func() {
		for {
			frame, err := proto.ReadFrame(conn)
			if err != nil {
				errCh <- err
				return
			}
			switch frame.Type {
			case proto.FrameData:
				if _, err := p.Write(frame.Payload); err != nil {
					errCh <- err
					return
				}
			case proto.FrameResize:
				cols, rows, err := proto.DecodeResize(frame.Payload)
				if err != nil {
					log.Printf("%s: %v", remote, err)
					continue
				}
				if err := p.Resize(cols, rows); err != nil {
					log.Printf("%s: resize: %v", remote, err)
				}
			case proto.FramePing:
				_ = proto.WriteFrame(conn, proto.Frame{Type: proto.FramePong})
			default:
				// ignore unknown/reserved frame types (e.g. AUTH*, unused per spec §8)
			}
		}
	}()

	// Wait() polls the process handle directly and is the only reliable exit
	// signal: after the child exits, ConPTY's Read() can still block forever
	// because conhost keeps its end of the pipe open (see spec notes).
	go func() {
		code, err := p.Wait()
		if err != nil {
			errCh <- err
			return
		}
		exitCh <- code
	}()

	var exitCode uint32
	select {
	case code := <-exitCh:
		exitCode = code
	case err := <-errCh:
		if err != io.EOF {
			log.Printf("%s: %v", remote, err)
		}
	}

	// Unblocks any goroutine still stuck in p.Read().
	if err := p.Close(); err != nil {
		log.Printf("%s: close pty: %v", remote, err)
	}

	if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameExit, Payload: proto.EncodeExit(exitCode)}); err != nil && err != io.EOF {
		log.Printf("%s: send EXIT: %v", remote, err)
	}
}
