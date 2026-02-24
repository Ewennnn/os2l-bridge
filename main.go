package main

import (
	_ "embed"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
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
	systray.Run(onReady, onExit)
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

func onExit() {
	// TODO exit application
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

	for {
		n, err := b.vdj.Read(buf)
		if err != nil {
			// Connection will be safe closed when new VirtualDJ connection established
			b.logger.Error("VirtualDJ disconnected", "err", err)
			b.mu.Lock()
			b.vdj = nil
			b.updateSystrayStatus()
			b.mu.Unlock()
			return
		}

		if !b.isSoundSwitchConnected() {
			continue // Ignore message
		}

		_, err = b.ss.Write(buf[:n])
		if err != nil {
			b.logger.Error("SoundSwitch disconnected", "err", err)

			// Lock to edit bridge state
			b.mu.Lock()
			safeClose(b.ss)
			b.ss = nil
			b.updateSystrayStatus()
			b.mu.Unlock()
		}
	}
}

func (b *OS2LBridge) pipeSoundSwitchToVDJ() {
	buf := make([]byte, 4096)
	for {
		n, err := b.ss.Read(buf)
		if err != nil {
			b.logger.Error("Soundswitch disconnected", "err", err)
			b.mu.Lock()
			b.ss = nil
			b.updateSystrayStatus()
			b.mu.Unlock()
			return
		}

		if !b.isVirtualDJConnected() {
			continue // Ignore messages
		}

		_, err = b.vdj.Write(buf[:n])
		if err != nil {
			b.logger.Error("VirtualDJ disconnected", "err", err)

			// Lock to edit bridge state
			b.mu.Lock()
			safeClose(b.ss)
			b.ss = nil
			b.updateSystrayStatus()
			b.mu.Unlock()
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
