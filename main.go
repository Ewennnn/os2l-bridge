package main

import (
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
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

	mu sync.Mutex
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

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler)
	bridge := &OS2LBridge{
		logger:    logger,
		vdjStatus: vdjStatus,
		ssStatus:  ssStatus,
	}

	go bridge.start()
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

func (b *OS2LBridge) start() {
	// Initialize Zeroconf mDNS server
	server, err := zeroconf.Register(
		"OS2L-Bridge",      // Service name (used for display only)
		"_os2l._tcp",       // Service type & protocol (needed and strict : https://os2l.org/)
		"local.",           // Service exposed only on local subnetwork
		8000,               // Service exposed on port 8000
		[]string{"txtv=0"}, // Define settings used by clients before connecting (optional for OS2L but good practice for mDNS)
		nil,                // No network interface specified, all networking hardware will be used
	)

	if err != nil {
		b.logger.Error("Error on mDNS initialisation", err)
		return
	}
	defer server.Shutdown()

	go b.listenForVDJ()
	go b.connectToSoundSwitch()

	select {} // Run forever
}

func (b *OS2LBridge) listenForVDJ() {
	listener, err := net.Listen("tcp", vdjListenAddr)
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
		b.updateSystrayStatus()
		b.mu.Unlock()

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
			b.updateSystrayStatus()
			b.mu.Unlock()

			return
		}

		messages := splitOS2LMessages(&streamBuffer, buf[:n])

		for _, msg := range messages {
			// Log individual JSON message
			b.logOS2L("VDJ -> SS", []byte(msg))

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
			b.updateSystrayStatus()
			b.mu.Unlock()

			return
		}

		messages := splitOS2LMessages(&streamBuffer, buf[:n])

		for _, msg := range messages {
			// Log individual JSON message
			b.logOS2L("SS -> VDJ", []byte(msg))

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

func (b *OS2LBridge) logOS2L(direction string, msg []byte) {
	if len(msg) == 0 {
		return
	}

	clean := sanitizeJSON(string(msg))

	if json.Valid([]byte(clean)) {
		var obj any
		if err := json.Unmarshal([]byte(clean), &obj); err == nil {
			pretty, _ := json.Marshal(obj)

			b.logger.Info(
				"OS2L message",
				"direction", direction,
				"json", string(pretty),
			)
			return
		}
	}

	// fallback si JSON invalide
	b.logger.Warn(
		"OS2L raw message",
		"direction", direction,
		"data", clean,
	)
}

func sanitizeJSON(s string) string {
	return strings.TrimSpace(
		strings.ReplaceAll(
			strings.ReplaceAll(s, "\n", ""),
			"\r",
			"",
		),
	)
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
