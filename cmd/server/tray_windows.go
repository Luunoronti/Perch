//go:build windows

// System-tray mode for perch-server. Implemented directly against the Win32
// API (no CGO, no third-party GUI dependency) so it keeps compiling under
// CGO_ENABLED=0 GOOS=windows like the rest of the project.
//
// The model is a hidden message-only window that owns a notification-area
// icon. Left/right clicking the icon pops up a menu; the server's accept
// loop runs on a background goroutine while this file owns the main OS
// thread's message pump.
package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"perch/internal/config"
)

//go:embed icon.ico
var iconICO []byte

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")

	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	procFreeConsole      = kernel32.NewProc("FreeConsole")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	// attachParentProcess is ATTACH_PARENT_PROCESS ((DWORD)-1) for AttachConsole.
	attachParentProcess = 0xFFFFFFFF

	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// trayHasConsole reports whether we're attached to a console -- i.e. we were
// launched from a terminal rather than from Explorer or the autostart Run
// key. It's the signal to relaunch ourselves detached (see relaunchDetached).
func trayHasConsole() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

// relaunchDetached starts a fresh `perch-server -tray` fully detached from
// the current console and (best effort) broken out of the terminal's job
// object, so the launching shell doesn't wait on it and it survives the
// terminal closing. The caller exits immediately afterwards.
func relaunchDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	start := func(flags uint32) error {
		cmd := exec.Command(exe, "-tray")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, NoInheritHandles: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release() // detach: never Wait on it
	}
	// Try to break out of the shell's job object (Windows Terminal kills its
	// whole process tree on close otherwise). If the job forbids breakaway,
	// CreateProcess fails, so retry without that flag.
	if err := start(detachedProcess | createNewProcessGroup | createBreakawayFromJob); err != nil {
		return start(detachedProcess | createNewProcessGroup)
	}
	return nil
}

const (
	wmDestroy    = 0x0002
	wsOverlapped = 0x00000000

	// wmTrayCallback is the private message the shell posts to our window for
	// icon mouse events; the low word of lParam is the actual mouse message.
	wmTrayCallback = 0x8000 + 1 // WM_APP + 1
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	idiApplication = 32512
	idcArrow       = 32512

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008
	mfGrayed    = 0x00000001

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	menuOpenFolder = 1
	menuAutostart  = 2
	menuQuit       = 3
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// notifyIconDataW mirrors the modern NOTIFYICONDATAW; cbSize is set to the
// full size so we don't have to care about shell version quirks.
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// tray holds the process-wide tray state so the window procedure (a C
// callback with no closure) can reach it.
type trayState struct {
	hwnd       windows.Handle
	nid        notifyIconDataW
	listenAddr string
}

var tray trayState

// runTray installs the notification-area icon and runs the Windows message
// loop, launching serve() on a background goroutine. It returns when the
// user picks Quit (or the loop errors).
func runTray(listenAddr string, serve func()) error {
	// The message pump must own a single OS thread for its whole lifetime.
	runtime.LockOSThread()

	// Detach any inherited console so no stray window lingers behind the
	// tray icon (e.g. when launched from Explorer or the Run key).
	procFreeConsole.Call()

	tray.listenAddr = listenAddr

	hInst, _, _ := procGetModuleHandleW.Call(0)
	hInstance := windows.Handle(hInst)

	className, err := windows.UTF16PtrFromString("PerchServerTrayWindow")
	if err != nil {
		return err
	}

	hIcon := loadEmbeddedIcon()
	if hIcon == 0 {
		stock, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
		hIcon = windows.Handle(stock)
	}
	hCursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))

	wc := wndClassExW{
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     hInstance,
		hIcon:         windows.Handle(hIcon),
		hCursor:       windows.Handle(hCursor),
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassEx: %w", e)
	}

	hwnd, _, e := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		wsOverlapped,
		0, 0, 0, 0,
		0, 0, uintptr(hInstance), 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", e)
	}
	tray.hwnd = windows.Handle(hwnd)

	tray.nid = notifyIconDataW{
		hWnd:             tray.hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCallback,
		hIcon:            windows.Handle(hIcon),
	}
	tray.nid.cbSize = uint32(unsafe.Sizeof(tray.nid))
	setTip(&tray.nid, "perch-server — listening on "+listenAddr)
	if r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&tray.nid))); r == 0 {
		return fmt.Errorf("Shell_NotifyIcon(ADD): %w", e)
	}
	defer procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&tray.nid)))

	go serve()

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

// attachConsole reattaches the process to the console of whatever launched
// it. The server is built for the GUI subsystem (-H=windowsgui) so no
// console flashes up in tray mode; the price is that console/debug mode
// starts detached too. Reattaching to the parent console (and repointing the
// standard streams at it) makes plain `perch-server` print its logs again
// when run from a terminal. When there is no parent console (e.g. launched
// from Explorer) it is a harmless no-op.
func attachConsole() {
	r, _, _ := procAttachConsole.Call(attachParentProcess)
	if r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
		log.SetOutput(out)
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
}

func wndProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTrayCallback:
		switch lParam & 0xffff {
		case wmLButtonUp, wmRButtonUp:
			showMenu(hwnd)
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

func showMenu(hwnd windows.Handle) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendMenu(menu, mfString|mfGrayed, 0, "perch-server — "+tray.listenAddr)
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, menuOpenFolder, "Open config folder")

	autoFlags := uintptr(mfString)
	if autostartEnabled() {
		autoFlags |= mfChecked
	}
	appendMenu(menu, autoFlags, menuAutostart, "Start with Windows")

	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, menuQuit, "Quit perch-server")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required so the menu dismisses correctly when clicking elsewhere.
	procSetForegroundWindow.Call(uintptr(hwnd))

	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmLeftAlign|tpmRightButton|tpmReturnCmd,
		uintptr(pt.x), uintptr(pt.y),
		0, uintptr(hwnd), 0,
	)
	// Companion to the SetForegroundWindow call above (classic Win32 tray
	// menu fix): nudge the window so a subsequent click is handled.
	procPostMessageW.Call(uintptr(hwnd), 0, 0, 0)

	switch cmd {
	case menuOpenFolder:
		openConfigFolder()
	case menuAutostart:
		toggleAutostart()
	case menuQuit:
		procDestroyWindow.Call(uintptr(hwnd))
	}
}

func appendMenu(menu, flags, id uintptr, text string) {
	var p uintptr
	if text != "" {
		t, err := windows.UTF16PtrFromString(text)
		if err != nil {
			return
		}
		p = uintptr(unsafe.Pointer(t))
	}
	procAppendMenuW.Call(menu, flags, id, p)
}

// loadEmbeddedIcon parses the embedded .ico, picks the image nearest 32px
// and turns it into an HICON. Returns 0 on any problem so the caller can
// fall back to a stock icon. CreateIconFromResourceEx accepts the per-image
// payload directly, including PNG-compressed images (Vista+).
func loadEmbeddedIcon() windows.Handle {
	if len(iconICO) < 6 {
		return 0
	}
	count := int(binary.LittleEndian.Uint16(iconICO[4:6]))
	if count == 0 || len(iconICO) < 6+16*count {
		return 0
	}
	const want = 32
	best, bestDiff := -1, 1<<31
	for i := 0; i < count; i++ {
		e := iconICO[6+16*i:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		diff := w - want
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff, best = diff, i
		}
	}
	e := iconICO[6+16*best:]
	size := binary.LittleEndian.Uint32(e[8:12])
	offset := binary.LittleEndian.Uint32(e[12:16])
	if int(offset)+int(size) > len(iconICO) || size == 0 {
		return 0
	}
	img := iconICO[offset : offset+size]
	h, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&img[0])),
		uintptr(size),
		1,          // fIcon
		0x00030000, // dwVer
		0, 0,       // desired size 0 = use the image's own
		0, // LR_DEFAULTCOLOR
	)
	return windows.Handle(h)
}

func setTip(nid *notifyIconDataW, tip string) {
	u := windows.StringToUTF16(tip)
	if len(u) > len(nid.szTip) {
		u = u[:len(nid.szTip)]
		u[len(u)-1] = 0 // keep it null-terminated
	}
	copy(nid.szTip[:], u)
}

func openConfigFolder() {
	dir, err := config.Dir()
	if err != nil {
		log.Printf("tray: config dir: %v", err)
		return
	}
	if err := exec.Command("explorer.exe", dir).Start(); err != nil {
		log.Printf("tray: open folder: %v", err)
	}
}

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "perch-server"
)

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	return err == nil
}

func toggleAutostart() {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Printf("tray: open Run key: %v", err)
		return
	}
	defer k.Close()

	if _, _, err := k.GetStringValue(runValueName); err == nil {
		if err := k.DeleteValue(runValueName); err != nil {
			log.Printf("tray: disable autostart: %v", err)
		}
		return
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("tray: executable path: %v", err)
		return
	}
	if err := k.SetStringValue(runValueName, `"`+exe+`" -tray`); err != nil {
		log.Printf("tray: enable autostart: %v", err)
	}
}
