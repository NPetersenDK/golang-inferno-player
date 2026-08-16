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

// UsrvclockFrame is the exact 40-byte little-endian frame of usrvclock v1.0.
// Spec: https://gitlab.com/lumifaza/usrvclock
type UsrvclockFrame struct {
	Magic     [2]byte // 'V', 'C'
	Major     uint16  // 1
	Minor     uint16  // 0
	Flags     int16   // 0x0001 = clock valid
	ClockID   int64   // 0 = CLOCK_REALTIME
	LastSync  int64   // system monotonic time, ns
	Shift     int64   // grandmaster phase shift, ns
	FreqScale float64 // fractional rate correction
}

// UsrvclockServer implements the userspace virtual clock Unix datagram server.
type UsrvclockServer struct {
	socketPath string
	conn       *net.UnixConn
	clients    map[string]*net.UnixAddr
	discipline *ClockDiscipline
	mu         sync.Mutex
	stopChan   chan struct{}
}

// StartUsrvclockServer binds a Unix datagram socket and serves clock overlays to
// Inferno from the shared ClockDiscipline.
func StartUsrvclockServer(socketPath string, discipline *ClockDiscipline) (*UsrvclockServer, error) {
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
		discipline: discipline,
		stopChan:   make(chan struct{}),
	}

	go srv.readLoop()
	go srv.broadcastLoop()

	log.Printf("[Clock Server] usrvclock media clock server active on %s", socketPath)
	return srv, nil
}

func (s *UsrvclockServer) makeFrame() []byte {
	now := time.Now().UnixNano()

	var shiftNs int64
	var freqScale float64
	if s.discipline != nil {
		shiftNs, freqScale = s.discipline.Overlay()
	}

	frame := UsrvclockFrame{
		Magic:    [2]byte{'V', 'C'},
		Major:    1,
		Minor:    0,
		Flags:    1,
		ClockID:  0,
		LastSync: now,
		// Inferno computes now_ns = CLOCK_REALTIME + Shift + elapsed*FreqScale.
		Shift:     shiftNs,
		FreqScale: freqScale,
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
