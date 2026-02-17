package main

import (
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	vdjListenAddr   = ":8000"
	soundSwitchAddr = "127.0.0.1:60594"
)

type OS2LBridge struct {
	logger *slog.Logger
	vdj    net.Conn
	ss     net.Conn

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

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler)

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
		logger.Error("Error on mDNS initialisation", err)
		return
	}
	defer server.Shutdown()

	bridge := &OS2LBridge{
		logger: logger,
	}
	go bridge.listenForVDJ()
	go bridge.connectToSoundSwitch()

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
		b.mu.Unlock()

		b.logger.Info("SoundSwitch connection established", "ssAddr", conn.RemoteAddr().String())
	}
}

func (b *OS2LBridge) pipeVDJToSoundSwitch() {
	buf := make([]byte, 4096)

	for {
		n, err := b.vdj.Read(buf)
		if err != nil {
			// Connection will be safe closed when new VirtualDJ connection established
			b.logger.Error("VirtualDJ disconnected", "err", err)
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
			b.mu.Unlock()
		}
	}
}

func safeClose(closer io.Closer) {
	err := closer.Close()
	if err != nil {
		log.Panicln("Failed to close file", err)
	}
}
