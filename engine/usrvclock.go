package engine

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// UsrvclockFrame matches the exact 40-byte binary frame of the usrvclock protocol v1.0.
// Protocol spec: https://gitlab.com/lumifaza/usrvclock
type UsrvclockFrame struct {
	Magic     [2]byte // 'V', 'C'
	Major     uint16  // 1
	Minor     uint16  // 0
	Flags     int16   // 0x0001 (Clock valid)
	ClockID   int64   // 4 (CLOCK_MONOTONIC_RAW) or 1 (CLOCK_MONOTONIC)
	LastSync  int64   // System monotonic time in nanoseconds
	Shift     int64   // Offset shift in nanoseconds
	FreqScale float64 // Frequency drift correction
}

// UsrvclockServer implements the userspace virtual clock Unix datagram server.
type UsrvclockServer struct {
	socketPath string
	conn       *net.UnixConn
	clients    map[string]*net.UnixAddr
	mu         sync.Mutex
	stopChan   chan struct{}
}

// StartUsrvclockServer binds a Unix datagram socket and continuously serves ClockOverlays to Inferno.
func StartUsrvclockServer(socketPath string) (*UsrvclockServer, error) {
	_ = os.Remove(socketPath)

	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0777)

	srv := &UsrvclockServer{
		socketPath: socketPath,
		conn:       conn,
		clients:    make(map[string]*net.UnixAddr),
		stopChan:   make(chan struct{}),
	}

	go srv.readLoop()
	go srv.broadcastLoop()

	log.Printf("[Clock Server] Embedded usrvclock media clock server active on %s", socketPath)
	return srv, nil
}

func (s *UsrvclockServer) makeFrame() []byte {
	now := time.Now().UnixNano()
	frame := UsrvclockFrame{
		Magic:     [2]byte{'V', 'C'},
		Major:     1,
		Minor:     0,
		Flags:     1, // 0x0001 (Valid clock overlay)
		ClockID:   0, // 0 = CLOCK_REALTIME (matches PTP network epoch)
		LastSync:  now,
		Shift:     now,
		FreqScale: 0.0,
	}

	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.LittleEndian, frame)
	return buf.Bytes()
}

func (s *UsrvclockServer) readLoop() {
	buf := make([]byte, 1024)
	for {
		_, remoteAddr, err := s.conn.ReadFromUnix(buf)
		if err != nil {
			select {
			case <-s.stopChan:
				return
			default:
				log.Printf("[Clock Server] Connection read error: %v", err)
				return
			}
		}

		if remoteAddr != nil {
			// Reply immediately with a valid ClockOverlay
			frameBytes := s.makeFrame()
			_, _ = s.conn.WriteToUnix(frameBytes, remoteAddr)

			s.mu.Lock()
			s.clients[remoteAddr.String()] = remoteAddr
			s.mu.Unlock()
		}
	}
}

func (s *UsrvclockServer) broadcastLoop() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			frameBytes := s.makeFrame()
			s.mu.Lock()
			for key, client := range s.clients {
				if _, err := s.conn.WriteToUnix(frameBytes, client); err != nil {
					delete(s.clients, key)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *UsrvclockServer) Stop() {
	close(s.stopChan)
	if s.conn != nil {
		_ = s.conn.Close()
	}
	_ = os.Remove(s.socketPath)
}
