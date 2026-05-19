package main

import (
	"context"
	_ "embed"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"github.com/grandcat/zeroconf"
)

const (
	vdjListenAddr   = ":8000"
	soundSwitchAddr = "127.0.0.1:60594"

	vdjConnected    = "VirtualDJ -> Connecté"
	vdjNotConnected = "VirtualDJ -> Non connecté"
	ssConnected     = "SoundSwitch -> Connecté"
	ssNotConnected  = "SoundSwitch -> Non connecté"
)

type Icon []byte

//go:embed icons/icon1.ico
var bridgeWait Icon

//go:embed icons/icon2.ico
var connectedToVirtualDJ Icon

//go:embed icons/icon3.ico
var connectedToSoundSwitch Icon

//go:embed icons/icon4.ico
var bridgeReady Icon

type OS2LBridge struct {
	logger *slog.Logger
	vdj    net.Conn
	ss     net.Conn

	vdjStatus *systray.MenuItem
	ssStatus  *systray.MenuItem

	mu    sync.Mutex
	state *BridgeState
	ui    *UIRenderer
}

// Mutex must be used before and after call this function
func (b *OS2LBridge) isVirtualDJConnected() bool {
	return b.vdj != nil
}

// Mutex must be used before and after call this function
func (b *OS2LBridge) isSoundSwitchConnected() bool {
	return b.ss != nil
}

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(bridgeWait)
	systray.SetTitle("OS2L Bridge")
	systray.SetTooltip("OS2L Bridge (VirtualDJ ↔ SoundSwitch)")

	vdjStatus := systray.AddMenuItem(vdjNotConnected, "")
	ssStatus := systray.AddMenuItem(ssNotConnected, "")

	systray.AddSeparator()

	mQuit := systray.AddMenuItem("Quitter", "Fermer le bridge")

	// Simple logger without file output
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})
	logger := slog.New(handler)

	state := NewBridgeState()
	ui := NewUIRenderer(state)

	bridge := &OS2LBridge{
		logger:    logger,
		vdjStatus: vdjStatus,
		ssStatus:  ssStatus,
		state:     state,
		ui:        ui,
	}

	go bridge.start()
	go bridge.updateUIDisplay()
	go func() {
		select {
		case <-mQuit.ClickedCh:
			systray.Quit()
		}
	}()
}

func (b *OS2LBridge) updateSystrayStatus() {
	if b.isVirtualDJConnected() && b.isSoundSwitchConnected() {
		systray.SetIcon(bridgeReady)
		b.vdjStatus.SetTitle(vdjConnected)
		b.ssStatus.SetTitle(ssConnected)
	} else if b.isVirtualDJConnected() {
		systray.SetIcon(connectedToVirtualDJ)
		b.vdjStatus.SetTitle(vdjConnected)
		b.ssStatus.SetTitle(ssNotConnected)
	} else if b.isSoundSwitchConnected() {
		systray.SetIcon(connectedToSoundSwitch)
		b.vdjStatus.SetTitle(vdjNotConnected)
		b.ssStatus.SetTitle(ssConnected)
	} else {
		systray.SetIcon(bridgeWait)
		b.vdjStatus.SetTitle(vdjNotConnected)
		b.ssStatus.SetTitle(ssNotConnected)
	}
}

// updateUIDisplay updates the terminal UI display every second
func (b *OS2LBridge) updateUIDisplay() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		b.ui.RenderStatus()
	}
}

func (b *OS2LBridge) start() {
	// Initialize Zeroconf mDNS server (optional - continues if it fails)
	server, err := zeroconf.Register(
		"OS2L-Bridge",      // Service name (used for display only)
		"_os2l._tcp",       // Service type & protocol (needed and strict : https://os2l.org/)
		"local.",           // Service exposed only on local subnetwork
		8000,               // Service exposed on port 8000
		[]string{"txtv=0"}, // Define settings used by clients before connecting (optional for OS2L but good practice for mDNS)
		nil,                // No network interface specified, all networking hardware will be used
	)

	if err != nil {
		// mDNS registration failed, but we can continue without it
		b.logger.Warn("mDNS registration failed (this is optional)", "err", err)
	} else {
		defer server.Shutdown()
	}

	go b.listenForVDJ()
	go b.connectToSoundSwitch()

	select {} // Run forever
}

func (b *OS2LBridge) listenForVDJ() {
	lc := net.ListenConfig{
		Control: reuseAddr,
	}

	listener, err := lc.Listen(context.Background(), "tcp", vdjListenAddr)
	if err != nil {
		b.logger.Error("Error on VDJ connection initialisation", "err", err)
		os.Exit(1)
	}

	b.logger.Info("Waiting for VirtualDJ connection on port " + vdjListenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			b.logger.Error("Error on VDJ connection accepting", "err", err)
			continue
		}

		// Lock to edit bridge state
		b.mu.Lock()
		if b.isVirtualDJConnected() {
			safeClose(b.vdj)
		}
		b.vdj = conn
		b.state.VDJConnected.Store(true)
		b.mu.Unlock()
		b.updateSystrayStatus()
		b.logger.Info("VirtualDJ connection established", "vdjAddr", conn.RemoteAddr().String())

		go b.pipeVDJToSoundSwitch()
	}
}

func (b *OS2LBridge) connectToSoundSwitch() {
	for {
		// Lock to read bridge state
		b.mu.Lock()
		if b.isSoundSwitchConnected() {
			b.mu.Unlock()
			time.Sleep(time.Second)
			continue
		}
		b.mu.Unlock()

		conn, err := net.Dial("tcp", soundSwitchAddr)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		// Lock to edit bridge state
		b.mu.Lock()
		b.ss = conn
		b.state.SSConnected.Store(true)
		b.state.SSSubscribeCount.Store(0)
		b.state.SSSubscribed.Store(false)
		b.mu.Unlock()
		b.updateSystrayStatus()

		b.logger.Info("SoundSwitch connection established", "ssAddr", conn.RemoteAddr().String())
		go b.pipeSoundSwitchToVDJ()
	}
}

func (b *OS2LBridge) pipeVDJToSoundSwitch() {
	buf := make([]byte, 4096)

	var streamBuffer strings.Builder

	for {
		n, err := b.vdj.Read(buf)
		if err != nil {
			b.logger.Error("VirtualDJ disconnected", "err", err)

			b.mu.Lock()
			b.vdj = nil
			b.state.VDJConnected.Store(false)
			b.updateSystrayStatus()
			b.mu.Unlock()

			return
		}

		messages := splitOS2LMessages(&streamBuffer, buf[:n])

		for _, msg := range messages {
			// Update state with OS2L message (non-blocking)
			b.state.UpdateFromOS2LMessage("VDJ -> SS", msg)

			// Always consume messages even if SS disconnected
			if !b.isSoundSwitchConnected() {
				continue
			}

			// Forward ONE JSON PER WRITE
			_, err := b.ss.Write([]byte(msg + "\n"))
			if err != nil {
				b.logger.Error("SoundSwitch disconnected", "err", err)

				b.mu.Lock()
				safeClose(b.ss)
				b.ss = nil
				b.state.SSConnected.Store(false)
				b.state.SSSubscribed.Store(false)
				b.updateSystrayStatus()
				b.mu.Unlock()

				break
			}
		}
	}
}

func (b *OS2LBridge) pipeSoundSwitchToVDJ() {
	buf := make([]byte, 4096)

	var streamBuffer strings.Builder

	for {
		n, err := b.ss.Read(buf)
		if err != nil {
			b.logger.Error("SoundSwitch disconnected", "err", err)

			b.mu.Lock()
			b.ss = nil
			b.state.SSConnected.Store(false)
			b.state.SSSubscribed.Store(false)
			b.updateSystrayStatus()
			b.mu.Unlock()

			return
		}

		messages := splitOS2LMessages(&streamBuffer, buf[:n])

		for _, msg := range messages {
			// Update state with OS2L message (non-blocking)
			b.state.UpdateFromOS2LMessage("SS -> VDJ", msg)

			if !b.isVirtualDJConnected() {
				continue
			}

			// Forward ONE JSON PER WRITE
			_, err := b.vdj.Write([]byte(msg + "\n"))
			if err != nil {
				b.logger.Error("VirtualDJ disconnected", "err", err)

				b.mu.Lock()
				safeClose(b.vdj)
				b.vdj = nil
				b.state.VDJConnected.Store(false)
				b.updateSystrayStatus()
				b.mu.Unlock()

				break
			}
		}
	}
}

func safeClose(closer io.Closer) {
	if closer == nil {
		return
	}

	err := closer.Close()
	if err != nil {
		log.Panicln("Failed to close file", err)
	}
}

func splitOS2LMessages(buffer *strings.Builder, incoming []byte) []string {
	buffer.Write(incoming)

	data := buffer.String()

	var (
		messages []string
		current  []rune

		depth    int
		inString bool
		escaped  bool
	)

	for _, r := range data {
		current = append(current, r)

		if escaped {
			escaped = false
			continue
		}

		switch r {
		case '\\':
			if inString {
				escaped = true
			}

		case '"':
			inString = !inString

		case '{':
			if !inString {
				depth++
			}

		case '}':
			if !inString {
				depth--

				// Complete JSON object detected
				if depth == 0 {
					msg := strings.TrimSpace(string(current))
					if msg != "" {
						messages = append(messages, msg)
					}

					current = current[:0]
				}
			}
		}
	}

	// Keep incomplete fragment for next TCP read
	buffer.Reset()
	buffer.WriteString(string(current))

	return messages
}

// reuseAddr sets SO_REUSEADDR on the listener socket
func reuseAddr(network, address string, c syscall.RawConn) error {
	var opErr error
	if err := c.Control(func(fd uintptr) {
		opErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return opErr
}
