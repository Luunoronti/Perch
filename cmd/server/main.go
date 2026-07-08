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
	"perch/internal/session"
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

	mgr := session.NewManager()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, cfg, mgr)
	}
}

func handleConn(conn net.Conn, cfg config.ServerConfig, mgr *session.Manager) {
	remote := conn.RemoteAddr()
	log.Printf("connection from %s", remote)
	defer func() {
		conn.Close()
		log.Printf("connection from %s closed", remote)
	}()

	// Handshake: SESSION (name, empty = ephemeral) then RESIZE (spec §4.3).
	frame, err := proto.ReadFrame(conn)
	if err != nil {
		log.Printf("%s: read SESSION frame: %v", remote, err)
		return
	}
	if frame.Type != proto.FrameSession {
		log.Printf("%s: expected SESSION as first frame, got %v", remote, frame.Type)
		return
	}
	name := string(frame.Payload)

	frame, err = proto.ReadFrame(conn)
	if err != nil {
		log.Printf("%s: read RESIZE frame: %v", remote, err)
		return
	}
	if frame.Type != proto.FrameResize {
		log.Printf("%s: expected RESIZE as second frame, got %v", remote, frame.Type)
		return
	}
	cols, rows, err := proto.DecodeResize(frame.Payload)
	if err != nil {
		log.Printf("%s: %v", remote, err)
		return
	}

	sess, existed, err := mgr.Attach(name, cfg.Shell, cfg.ShellArgs, cols, rows)
	if err != nil {
		log.Printf("%s: start session: %v", remote, err)
		return
	}
	if existed {
		log.Printf("%s: attached to persistent session %q", remote, name)
		if err := sess.Resize(cols, rows); err != nil {
			log.Printf("%s: resize: %v", remote, err)
		}
	} else if name != "" {
		log.Printf("%s: started persistent session %q", remote, name)
	}

	out := sess.Subscribe()
	defer sess.Unsubscribe(out)

	connErrCh := make(chan error, 1)

	// conn frames -> DATA -> session.Write ; RESIZE -> session.Resize
	go func() {
		for {
			frame, err := proto.ReadFrame(conn)
			if err != nil {
				connErrCh <- err
				return
			}
			switch frame.Type {
			case proto.FrameData:
				if _, err := sess.Write(frame.Payload); err != nil {
					connErrCh <- err
					return
				}
			case proto.FrameResize:
				cols, rows, err := proto.DecodeResize(frame.Payload)
				if err != nil {
					log.Printf("%s: %v", remote, err)
					continue
				}
				if err := sess.Resize(cols, rows); err != nil {
					log.Printf("%s: resize: %v", remote, err)
				}
			case proto.FramePing:
				_ = proto.WriteFrame(conn, proto.Frame{Type: proto.FramePong})
			default:
				// ignore unknown/reserved frame types (e.g. AUTH*, unused per spec §8)
			}
		}
	}()

	// session output -> DATA frames -> conn
	writeErrCh := make(chan error, 1)
	go func() {
		for payload := range out {
			if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameData, Payload: payload}); err != nil {
				writeErrCh <- err
				return
			}
		}
	}()

	select {
	case <-sess.Done():
		if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameExit, Payload: proto.EncodeExit(sess.ExitCode())}); err != nil && err != io.EOF {
			log.Printf("%s: send EXIT: %v", remote, err)
		}
	case err := <-connErrCh:
		if err != io.EOF {
			log.Printf("%s: %v", remote, err)
		}
		if name == "" {
			// Ephemeral session: nobody else can ever reattach, so the
			// shell dies with its one and only client.
			sess.Terminate()
		}
	case err := <-writeErrCh:
		log.Printf("%s: %v", remote, err)
		if name == "" {
			sess.Terminate()
		}
	}
}
