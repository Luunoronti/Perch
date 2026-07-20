// perch-server: remote pwsh terminal server. MUST run inside the logged-in
// user's interactive desktop session — see remote-pwsh-terminal-spec.md §9.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"

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

	// First frame: either SESSION (name, empty = ephemeral) -- normal
	// attach flow, followed by RESIZE (spec §4.3) -- or LIST_SESSIONS, a
	// one-shot request that gets a listing back and no shell at all.
	frame, err := proto.ReadFrame(conn)
	if err != nil {
		log.Printf("%s: read first frame: %v", remote, err)
		return
	}
	if frame.Type == proto.FrameListSessions {
		if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameSessionList, Payload: []byte(formatSessionList(mgr.List()))}); err != nil {
			log.Printf("%s: send session list: %v", remote, err)
		}
		return
	}
	if frame.Type != proto.FrameSession {
		log.Printf("%s: expected SESSION or LIST_SESSIONS as first frame, got %v", remote, frame.Type)
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
	} else if name != "" {
		log.Printf("%s: started persistent session %q", remote, name)
	}

	// Subscribe registers this client's initial size and re-fits the pty
	// to the smallest attached client (spec §6.3) -- important the moment
	// a second client joins with a smaller terminal than the first.
	out, backlog := sess.Subscribe(cols, rows)
	defer sess.Unsubscribe(out)

	if len(backlog) > 0 {
		if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameData, Payload: backlog}); err != nil {
			log.Printf("%s: send backlog: %v", remote, err)
			return
		}
	}

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
				if err := sess.Resize(out, cols, rows); err != nil {
					log.Printf("%s: resize: %v", remote, err)
				}
			case proto.FramePing:
				_ = proto.WriteFrame(conn, proto.Frame{Type: proto.FramePong})
			default:
				// ignore unknown/reserved frame types (e.g. AUTH*, unused per spec §8)
			}
		}
	}()

	// session output -> DATA frames -> conn ; applied pty size -> RESIZE
	// frames -> conn (so the client can wipe the margin of its terminal that
	// falls outside the shared session viewport -- see spec §6.3).
	writeErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case payload, ok := <-out.Data():
				if !ok {
					return
				}
				if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameData, Payload: payload}); err != nil {
					writeErrCh <- err
					return
				}
			case sz := <-out.Resized():
				cols, rows := sz.Cols, sz.Rows
				if err := proto.WriteFrame(conn, proto.Frame{Type: proto.FrameResize, Payload: proto.EncodeResize(cols, rows)}); err != nil {
					writeErrCh <- err
					return
				}
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

func formatSessionList(infos []session.Info) string {
	if len(infos) == 0 {
		return "no persistent sessions running\n"
	}
	var b strings.Builder
	for _, info := range infos {
		plural := "s"
		if info.Clients == 1 {
			plural = ""
		}
		fmt.Fprintf(&b, "%s (%d client%s attached)\n", info.Name, info.Clients, plural)
	}
	return b.String()
}
