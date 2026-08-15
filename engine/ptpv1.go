package engine

// Passive IEEE 1588-2002 (PTPv1) listener and clock discipline.
//
// Inferno stamps outgoing packets with CLOCK_REALTIME + the Shift we serve over
// usrvclock, and the receiver drops anything older than the flow latency. So
// Shift has to equal (grandmaster time - CLOCK_REALTIME), measured here from
// the grandmaster's own Sync / Follow_Up messages.
//
// Listen only: no Delay_Req, no BMCA, no stepping of the system clock. Path
// delay is therefore unmeasured and appears as a constant bias of tens of
// microseconds, against a 10 ms jitter buffer budget.

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	ptpMulticastIP = "224.0.1.129"
	ptpEventPort   = 319 // Sync, Delay_Req
	ptpGeneralPort = 320 // Follow_Up, Delay_Resp, Management

	ptpSyncLen     = 124
	ptpFollowUpLen = 52

	ptpCtlSync     = 0x00
	ptpCtlFollowUp = 0x02

	// Common header, IEEE 1588-2002 clause 6.2.2.
	offVersionPTP = 0
	offSourceUUID = 22
	offSourcePort = 28
	offSequenceID = 30
	offControl    = 32
	offFlags      = 34

	// Sync / Delay_Req body, clause 6.2.3.
	offSyncOriginSec  = 40
	offSyncOriginNsec = 44

	// Follow_Up body, clause 6.2.5.
	offFollowUpAssocSeq = 42
	offFollowUpSec      = 44
	offFollowUpNsec     = 48

	// Set when the real send timestamp comes in a separate Follow_Up.
	ptpFlagAssist = 0x0008

	ptpSampleWindow     = 64
	ptpSampleMaxAge     = 120 * time.Second
	ptpMinSamplesForFit = 4
	ptpPendingMaxAge    = 3 * time.Second
	ptpHoldover         = 30 * time.Second

	// Sanity bound on the rate difference between two crystal oscillators.
	ptpMaxDriftNsPerSec = 500_000.0 // 500 ppm
)

type ptpSample struct {
	localNs  int64 // CLOCK_REALTIME when the packet was received
	offsetNs int64 // grandmaster time - CLOCK_REALTIME
}

type pendingSync struct {
	localNs int64
	at      time.Time
}

// PTPStats is a snapshot of the listener state for the status API.
type PTPStats struct {
	Locked      bool    `json:"locked"`
	MasterID    string  `json:"master_id"`
	OffsetNs    int64   `json:"offset_ns"`
	SyncErrorNs int64   `json:"sync_error_ns"`
	DriftPPM    float64 `json:"drift_ppm"`
	Samples     int     `json:"samples"`
	SyncPackets uint64  `json:"sync_packets"`
	LastSyncAgo string  `json:"last_sync_ago"`
}

// PTPMonitor tracks the phase and rate of the Dante grandmaster relative to
// the local CLOCK_REALTIME.
type PTPMonitor struct {
	mu      sync.RWMutex
	samples []ptpSample

	fitT0 int64   // local time the fit is anchored at
	fitA  float64 // offset in ns at fitT0
	fitB  float64 // ns of offset gained per second of local time

	locked    bool
	twoStep   bool
	masterID  string
	syncCount uint64
	lastSync  time.Time

	pendMu  sync.Mutex
	pending map[string]pendingSync

	stopChan chan struct{}
	conns    []*net.UDPConn
}

// StartPTPMonitor joins the PTPv1 multicast group and begins measuring.
// It never fails hard: without a grandmaster on the wire the monitor simply
// never locks and the caller falls back to its static offset.
func StartPTPMonitor() *PTPMonitor {
	m := &PTPMonitor{
		pending:  make(map[string]pendingSync),
		stopChan: make(chan struct{}),
	}

	ifi := resolveDanteInterface()
	ifName := "all interfaces"
	if ifi != nil {
		ifName = ifi.Name
	}

	event, err := openPTPSocket(ifi, ptpEventPort)
	if err != nil {
		log.Printf("[PTP] cannot listen on %s:%d (%v) - falling back to static offset", ptpMulticastIP, ptpEventPort, err)
		return m
	}
	general, err := openPTPSocket(ifi, ptpGeneralPort)
	if err != nil {
		log.Printf("[PTP] cannot listen on %s:%d (%v) - falling back to static offset", ptpMulticastIP, ptpGeneralPort, err)
		_ = event.Close()
		return m
	}
	m.conns = []*net.UDPConn{event, general}

	go m.readLoop(event)
	go m.readLoop(general)
	go m.expirePending()

	log.Printf("[PTP] passive PTPv1 listener active on %s (%s ports %d/%d)", ifName, ptpMulticastIP, ptpEventPort, ptpGeneralPort)
	return m
}

func (m *PTPMonitor) Stop() {
	select {
	case <-m.stopChan:
		return
	default:
	}
	close(m.stopChan)
	for _, c := range m.conns {
		_ = c.Close()
	}
}

// Estimate returns the current grandmaster offset and the fractional rate
// difference, or ok=false while unlocked or in holdover.
func (m *PTPMonitor) Estimate() (offsetNs int64, freqScale float64, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.locked || time.Since(m.lastSync) > ptpHoldover {
		return 0, 0, false
	}
	elapsedSec := float64(time.Now().UnixNano()-m.fitT0) / 1e9
	return int64(m.fitA + m.fitB*elapsedSec), m.fitB / 1e9, true
}

func (m *PTPMonitor) Stats() PTPStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := PTPStats{
		Locked:      m.locked && time.Since(m.lastSync) <= ptpHoldover,
		MasterID:    m.masterID,
		DriftPPM:    m.fitB / 1000.0, // ns per second -> parts per million
		Samples:     len(m.samples),
		SyncPackets: m.syncCount,
		LastSyncAgo: "never",
	}
	if !m.lastSync.IsZero() {
		st.LastSyncAgo = time.Since(m.lastSync).Truncate(time.Millisecond).String()
	}
	if st.Locked {
		elapsedSec := float64(time.Now().UnixNano()-m.fitT0) / 1e9
		st.OffsetNs = int64(m.fitA + m.fitB*elapsedSec)
	}
	return st
}

func (m *PTPMonitor) readLoop(c *net.UDPConn) {
	buf := make([]byte, 512)
	oob := make([]byte, 128)
	for {
		n, rxNs, err := readPacketWithTimestamp(c, buf, oob)
		if err != nil {
			select {
			case <-m.stopChan:
				return
			default:
			}
			log.Printf("[PTP] read error: %v", err)
			return
		}
		m.handlePacket(buf[:n], rxNs)
	}
}

func (m *PTPMonitor) handlePacket(pkt []byte, rxNs int64) {
	if len(pkt) < ptpFollowUpLen || binary.BigEndian.Uint16(pkt[offVersionPTP:]) != 1 {
		return
	}
	srcID := fmt.Sprintf("%x:%d",
		pkt[offSourceUUID:offSourceUUID+6],
		binary.BigEndian.Uint16(pkt[offSourcePort:]))
	seq := binary.BigEndian.Uint16(pkt[offSequenceID:])
	key := srcID + "/" + strconv.Itoa(int(seq))

	switch pkt[offControl] {
	case ptpCtlSync:
		if len(pkt) < ptpSyncLen {
			return
		}
		m.noteMaster(srcID)
		assist := binary.BigEndian.Uint16(pkt[offFlags:])&ptpFlagAssist != 0
		if assist || m.isTwoStep() {
			m.pendMu.Lock()
			m.pending[key] = pendingSync{localNs: rxNs, at: time.Now()}
			m.pendMu.Unlock()
			return
		}
		// One-step: originTimestamp is the real send time.
		origin := ptpTimestamp(pkt, offSyncOriginSec, offSyncOriginNsec)
		m.addSample(rxNs, origin-rxNs)

	case ptpCtlFollowUp:
		// A Follow_Up proves this master is two-step, so from here on we wait
		// for one even if the assist flag is ever missing.
		m.setTwoStep()
		assoc := binary.BigEndian.Uint16(pkt[offFollowUpAssocSeq:])
		m.pendMu.Lock()
		p, found := m.pending[srcID+"/"+strconv.Itoa(int(assoc))]
		delete(m.pending, srcID+"/"+strconv.Itoa(int(assoc)))
		m.pendMu.Unlock()
		if !found {
			return
		}
		precise := ptpTimestamp(pkt, offFollowUpSec, offFollowUpNsec)
		m.addSample(p.localNs, precise-p.localNs)
	}
}

func ptpTimestamp(pkt []byte, secOff, nsecOff int) int64 {
	sec := int64(binary.BigEndian.Uint32(pkt[secOff:]))
	nsec := int64(binary.BigEndian.Uint32(pkt[nsecOff:]))
	return sec*1_000_000_000 + nsec
}

func (m *PTPMonitor) isTwoStep() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.twoStep
}

func (m *PTPMonitor) setTwoStep() {
	m.mu.Lock()
	m.twoStep = true
	m.mu.Unlock()
}

// noteMaster resets the fit when a different clock starts sending Sync, since
// the two grandmasters share no timeline.
func (m *PTPMonitor) noteMaster(srcID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncCount++
	if m.masterID == srcID {
		return
	}
	if m.masterID != "" {
		log.Printf("[PTP] grandmaster changed %s -> %s, discarding %d samples", m.masterID, srcID, len(m.samples))
	} else {
		log.Printf("[PTP] grandmaster detected: %s", srcID)
	}
	m.masterID = srcID
	m.samples = m.samples[:0]
	m.locked = false
}

func (m *PTPMonitor) addSample(localNs, offsetNs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastSync = time.Now()
	m.samples = append(m.samples, ptpSample{localNs: localNs, offsetNs: offsetNs})

	cutoff := localNs - int64(ptpSampleMaxAge)
	drop := 0
	for drop < len(m.samples) && m.samples[drop].localNs < cutoff {
		drop++
	}
	if extra := len(m.samples) - drop - ptpSampleWindow; extra > 0 {
		drop += extra
	}
	if drop > 0 {
		m.samples = append(m.samples[:0], m.samples[drop:]...)
	}

	m.refit()
}

// refit runs a least-squares fit of offset against local time, giving phase and
// rate in one step.
//
// Path delay biases one-way measurements one-sidedly: queueing only ever makes
// a packet look later, i.e. the offset too small. So the fit is repeated on the
// half of the window with the highest residuals, the least delayed packets.
func (m *PTPMonitor) refit() {
	n := len(m.samples)
	if n < ptpMinSamplesForFit {
		m.locked = false
		return
	}
	t0 := m.samples[n-1].localNs

	a, b, ok := lsqFit(m.samples, t0, nil)
	if !ok {
		return
	}

	residuals := make([]float64, n)
	for i, s := range m.samples {
		residuals[i] = float64(s.offsetNs) - (a + b*float64(s.localNs-t0)/1e9)
	}
	sorted := append([]float64(nil), residuals...)
	sort.Float64s(sorted)
	med := sorted[n/2]

	keep := make([]bool, n)
	kept := 0
	for i := range residuals {
		if residuals[i] >= med {
			keep[i] = true
			kept++
		}
	}
	if kept >= ptpMinSamplesForFit {
		if a2, b2, ok2 := lsqFit(m.samples, t0, keep); ok2 {
			a, b = a2, b2
		}
	}

	if math.IsNaN(a) || math.IsNaN(b) {
		return
	}
	b = math.Max(-ptpMaxDriftNsPerSec, math.Min(ptpMaxDriftNsPerSec, b))

	m.fitT0, m.fitA, m.fitB = t0, a, b
	if !m.locked {
		// Log the raw timestamps too, not just the difference: a Dante
		// grandmaster runs a free-running counter rather than wall time, and
		// seeing its absolute value is the only way to tell a correct reading
		// from a misparsed one.
		last := m.samples[n-1]
		masterNs := last.localNs + last.offsetNs
		log.Printf("[PTP] locked to %s: offset %.3f ms, drift %.2f ppm (%d samples)",
			m.masterID, a/1e6, b/1000.0, n)
		log.Printf("[PTP] grandmaster clock reads %d.%09d s, local CLOCK_REALTIME %d.%09d s",
			masterNs/1_000_000_000, masterNs%1_000_000_000,
			last.localNs/1_000_000_000, last.localNs%1_000_000_000)
	}
	m.locked = true
}

// lsqFit fits offset(ns) = a + b*seconds_since_t0 over the selected samples.
func lsqFit(samples []ptpSample, t0 int64, keep []bool) (a, b float64, ok bool) {
	var n, sx, sy, sxx, sxy float64
	for i, s := range samples {
		if keep != nil && !keep[i] {
			continue
		}
		x := float64(s.localNs-t0) / 1e9
		y := float64(s.offsetNs)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	if n < 2 {
		return 0, 0, false
	}
	den := n*sxx - sx*sx
	if den == 0 {
		// All samples share one timestamp: phase only, no rate information.
		return sy / n, 0, true
	}
	b = (n*sxy - sx*sy) / den
	a = (sy - b*sx) / n
	return a, b, true
}

func (m *PTPMonitor) expirePending() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stopChan:
			return
		case <-t.C:
			m.pendMu.Lock()
			for k, p := range m.pending {
				if time.Since(p.at) > ptpPendingMaxAge {
					delete(m.pending, k)
				}
			}
			m.pendMu.Unlock()
		}
	}
}

func openPTPSocket(ifi *net.Interface, port int) (*net.UDPConn, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(ptpMulticastIP), Port: port}
	c, err := net.ListenMulticastUDP("udp4", ifi, addr)
	if err != nil {
		return nil, err
	}
	_ = c.SetReadBuffer(1 << 18)
	if !enableRxTimestamps(c) {
		log.Printf("[PTP] kernel receive timestamps unavailable on port %d, using userspace arrival time", port)
	}
	return c, nil
}

// resolveDanteInterface accepts either an interface name or an IPv4 address,
// matching what entrypoint.sh puts in DANTE_INTERFACE / INFERNO_BIND_IP.
func resolveDanteInterface() *net.Interface {
	var want string
	for _, env := range []string{"DANTE_PTP_IFACE", "DANTE_INTERFACE", "INFERNO_BIND_IP"} {
		if v := os.Getenv(env); v != "" {
			want = v
			break
		}
	}
	if want == "" {
		return nil
	}
	if ifi, err := net.InterfaceByName(want); err == nil {
		return ifi
	}
	if ip := net.ParseIP(want); ip != nil {
		ifaces, _ := net.Interfaces()
		for i := range ifaces {
			addrs, _ := ifaces[i].Addrs()
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
					return &ifaces[i]
				}
			}
		}
	}
	log.Printf("[PTP] interface %q not found, listening on all interfaces", want)
	return nil
}

// ClockDiscipline turns the raw PTP measurement into the Shift and FreqScale
// values handed to Inferno, smoothing corrections so the media clock does not
// jump under a running flow.
type ClockDiscipline struct {
	ptp *PTPMonitor

	mu          sync.RWMutex
	shiftNs     int64
	freqScale   float64
	lastErrorNs int64
	acquired    bool
	lastTick    time.Time
	lastLog     time.Time

	staticNs   int64
	stepNs     int64
	maxSlewPPM float64

	// Closed once the grandmaster has been measured for the first time, so the
	// audio pipeline can hold off until the big initial step is behind it.
	acquiredCh   chan struct{}
	acquiredOnce sync.Once

	stopChan chan struct{}
}

// StartClockDiscipline begins tracking the grandmaster. Until PTP locks it
// serves the static DANTE_PTP_OFFSET_MS value, so Inferno's ALSA plugin still
// gets a valid overlay immediately and never hits its 5 second timeout.
func StartClockDiscipline(ptp *PTPMonitor) *ClockDiscipline {
	d := &ClockDiscipline{
		ptp:        ptp,
		staticNs:   envMillisToNs("DANTE_PTP_OFFSET_MS", 665),
		stepNs:     envMillisToNs("DANTE_PTP_STEP_MS", 5),
		maxSlewPPM: envFloat("DANTE_PTP_MAX_SLEW_PPM", 500),
		lastTick:   time.Now(),
		acquiredCh: make(chan struct{}),
		stopChan:   make(chan struct{}),
	}
	d.shiftNs = d.staticNs

	log.Printf("[Clock] discipline started: static fallback %.3f ms, step threshold %.3f ms, max slew %.0f ppm",
		float64(d.staticNs)/1e6, float64(d.stepNs)/1e6, d.maxSlewPPM)

	go d.run()
	return d
}

func (d *ClockDiscipline) Stop() {
	select {
	case <-d.stopChan:
	default:
		close(d.stopChan)
	}
}

// Overlay returns the values to put in the next usrvclock frame.
func (d *ClockDiscipline) Overlay() (shiftNs int64, freqScale float64) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.shiftNs, d.freqScale
}

// SyncError is how far the media clock we serve sits from the measured
// grandmaster, which is what decides whether a receiver accepts a packet. Not
// the network path delay, which a listen-only implementation never measures.
func (d *ClockDiscipline) SyncError() (ns int64, ok bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastErrorNs, d.acquired
}

func (d *ClockDiscipline) Locked() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.acquired
}

// WaitForLock blocks until the grandmaster has been measured, reporting whether
// that happened before the timeout.
//
// The first measurement steps the media clock by the grandmaster's whole epoch,
// which for a free-running counter is years. Inferno reacts by rebootstrapping
// every flow, so callers use this to land the step before anything transmits.
func (d *ClockDiscipline) WaitForLock(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-d.acquiredCh:
		return true
	case <-d.stopChan:
		return false
	case <-timer.C:
		return false
	}
}

func (d *ClockDiscipline) run() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-d.stopChan:
			return
		case <-t.C:
			d.tick()
		}
	}
}

func (d *ClockDiscipline) tick() {
	if d.ptp == nil {
		return
	}
	now := time.Now()
	target, freq, ok := d.ptp.Estimate()
	if !ok {
		// No grandmaster yet, or holdover expired: keep serving whatever we
		// last converged on rather than snapping back to the static value.
		d.mu.Lock()
		d.lastTick = now
		d.mu.Unlock()
		return
	}
	d.applyEstimate(target, freq, now)
}

func (d *ClockDiscipline) applyEstimate(target int64, freq float64, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	elapsed := now.Sub(d.lastTick)
	d.lastTick = now
	d.freqScale = freq

	err := target - d.shiftNs
	d.lastErrorNs = err
	if !d.acquired || absInt64(err) > d.stepNs {
		if d.acquired {
			log.Printf("[Clock] stepping media clock by %.3f ms (offset error exceeded %.3f ms)",
				float64(err)/1e6, float64(d.stepNs)/1e6)
		} else {
			log.Printf("[Clock] acquired grandmaster: shift %.3f ms (was static %.3f ms), drift %.2f ppm",
				float64(target)/1e6, float64(d.staticNs)/1e6, freq*1e6)
		}
		d.shiftNs = target
		d.acquired = true
		d.acquiredOnce.Do(func() { close(d.acquiredCh) })
		return
	}

	maxDelta := int64(d.maxSlewPPM * 1e-6 * float64(elapsed.Nanoseconds()))
	if maxDelta < 1 {
		maxDelta = 1
	}
	if err > maxDelta {
		err = maxDelta
	} else if err < -maxDelta {
		err = -maxDelta
	}
	d.shiftNs += err

	if now.Sub(d.lastLog) > 60*time.Second {
		d.lastLog = now
		log.Printf("[Clock] tracking grandmaster: sync error %.1f us, drift %.2f ppm, shift %.3f ms",
			float64(d.lastErrorNs)/1e3, freq*1e6, float64(d.shiftNs)/1e6)
	}
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func envMillisToNs(name string, defMs int64) int64 {
	ms := defMs
	if v := os.Getenv(name); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(parsed * 1e6)
		}
	}
	return ms * 1_000_000
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return parsed
		}
	}
	return def
}
