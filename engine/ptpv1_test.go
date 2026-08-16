package engine

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
	"time"
)

func buildSync(seq uint16, originNs int64, assist bool) []byte {
	p := make([]byte, ptpSyncLen)
	binary.BigEndian.PutUint16(p[offVersionPTP:], 1)
	copy(p[offSourceUUID:], []byte{0x00, 0x1d, 0xc1, 0x11, 0x22, 0x33})
	binary.BigEndian.PutUint16(p[offSourcePort:], 1)
	binary.BigEndian.PutUint16(p[offSequenceID:], seq)
	p[offControl] = ptpCtlSync
	if assist {
		binary.BigEndian.PutUint16(p[offFlags:], ptpFlagAssist)
	}
	binary.BigEndian.PutUint32(p[offSyncOriginSec:], uint32(originNs/1e9))
	binary.BigEndian.PutUint32(p[offSyncOriginNsec:], uint32(originNs%1e9))
	return p
}

func buildFollowUp(seq uint16, preciseNs int64) []byte {
	p := make([]byte, ptpFollowUpLen)
	binary.BigEndian.PutUint16(p[offVersionPTP:], 1)
	copy(p[offSourceUUID:], []byte{0x00, 0x1d, 0xc1, 0x11, 0x22, 0x33})
	binary.BigEndian.PutUint16(p[offSourcePort:], 1)
	binary.BigEndian.PutUint16(p[offSequenceID:], seq)
	p[offControl] = ptpCtlFollowUp
	binary.BigEndian.PutUint16(p[offFollowUpAssocSeq:], seq)
	binary.BigEndian.PutUint32(p[offFollowUpSec:], uint32(preciseNs/1e9))
	binary.BigEndian.PutUint32(p[offFollowUpNsec:], uint32(preciseNs%1e9))
	return p
}

// Two-step master 650 ms ahead, 30 ppm fast, 20-200 us of one-way delay.
func TestMonitorRecoversOffsetAndDrift(t *testing.T) {
	const (
		syncs      = 32
		trueOffset = 650 * time.Millisecond
		trueDrift  = 30e-6
	)

	m := &PTPMonitor{pending: make(map[string]pendingSync)}
	rng := rand.New(rand.NewSource(1))

	base := time.Now().UnixNano()
	for i := 0; i < syncs; i++ {
		localNs := base - int64(syncs-1-i)*int64(time.Second)
		offsetAt := int64(trueOffset) + int64(trueDrift*float64(localNs-base))
		delay := int64(20_000 + rng.Intn(180_000))

		// Master sends at (local + offset); we observe it `delay` later.
		sendNs := localNs + offsetAt
		m.handlePacket(buildSync(uint16(i), sendNs-500_000, true), localNs+delay)
		m.handlePacket(buildFollowUp(uint16(i), sendNs), 0)
	}

	gotOffset, gotFreq, ok := m.Estimate()
	if !ok {
		t.Fatalf("monitor did not lock after %d syncs", syncs)
	}
	if len(m.samples) != syncs {
		t.Fatalf("got %d samples, want %d", len(m.samples), syncs)
	}

	// Path delay biases the estimate low; the upper-half refit absorbs most of it.
	if err := math.Abs(float64(gotOffset - int64(trueOffset))); err > 300_000 {
		t.Errorf("offset error %.1f us, want within 300 us (got %d ns)", err/1000, gotOffset)
	}
	if err := math.Abs(gotFreq-trueDrift) * 1e6; err > 5 {
		t.Errorf("drift error %.2f ppm, want within 5 ppm (got %.2f ppm)", err, gotFreq*1e6)
	}
}

// A two-step master's Sync timestamp is coarse: never fit it without a Follow_Up.
func TestTwoStepSyncWithoutFollowUpIsIgnored(t *testing.T) {
	m := &PTPMonitor{pending: make(map[string]pendingSync)}
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		m.handlePacket(buildSync(uint16(i), now, true), now)
	}
	if len(m.samples) != 0 {
		t.Fatalf("got %d samples from Sync-only two-step master, want 0", len(m.samples))
	}
	if _, _, ok := m.Estimate(); ok {
		t.Fatal("monitor locked without any usable timestamp")
	}
}

func TestOneStepMasterUsesSyncTimestamp(t *testing.T) {
	m := &PTPMonitor{pending: make(map[string]pendingSync)}
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		localNs := now - int64(7-i)*int64(time.Second)
		m.handlePacket(buildSync(uint16(i), localNs+int64(100*time.Millisecond), false), localNs)
	}
	off, _, ok := m.Estimate()
	if !ok {
		t.Fatal("monitor did not lock on a one-step master")
	}
	if math.Abs(float64(off-int64(100*time.Millisecond))) > 50_000 {
		t.Errorf("offset %d ns, want ~100 ms", off)
	}
}

func TestGrandmasterChangeDiscardsWindow(t *testing.T) {
	m := &PTPMonitor{pending: make(map[string]pendingSync)}
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		localNs := now - int64(7-i)*int64(time.Second)
		m.handlePacket(buildSync(uint16(i), localNs+int64(100*time.Millisecond), false), localNs)
	}
	if _, _, ok := m.Estimate(); !ok {
		t.Fatal("expected lock before grandmaster change")
	}

	other := buildSync(99, now+int64(2*time.Second), false)
	copy(other[offSourceUUID:], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	m.handlePacket(other, now)

	if len(m.samples) != 1 {
		t.Fatalf("got %d samples after grandmaster change, want only the new one", len(m.samples))
	}
	if _, _, ok := m.Estimate(); ok {
		t.Fatal("monitor stayed locked across a grandmaster change")
	}
}

// After acquire, corrections slew so a running Dante flow sees no discontinuity.
func TestDisciplineSlewIsRateLimited(t *testing.T) {
	d := &ClockDiscipline{
		staticNs:   665 * 1e6,
		stepNs:     5 * 1e6,
		maxSlewPPM: 500,
		lastTick:   time.Now().Add(-100 * time.Millisecond),
		acquired:   true,
		shiftNs:    665 * 1e6,
		acquiredCh: make(chan struct{}),
		stopChan:   make(chan struct{}),
	}
	// 1 ms is below the step threshold, so it must be slewed.
	target := d.shiftNs + 1_000_000
	d.applyEstimate(target, 0, time.Now())

	moved := d.shiftNs - 665*1e6
	if moved <= 0 || moved > 60_000 {
		t.Errorf("slewed %d ns in 100 ms, want 0 < x <= 50 us (500 ppm)", moved)
	}
}

// Must not release before the first measurement, nor block forever without PTP.
func TestWaitForLockReleasesOnAcquire(t *testing.T) {
	d := &ClockDiscipline{
		staticNs:   665 * 1e6,
		stepNs:     5 * 1e6,
		maxSlewPPM: 500,
		lastTick:   time.Now(),
		acquiredCh: make(chan struct{}),
		stopChan:   make(chan struct{}),
	}

	if d.WaitForLock(10 * time.Millisecond) {
		t.Fatal("WaitForLock reported a lock before any measurement arrived")
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		d.applyEstimate(-1_785_713_638_806_000_000, 22.4e-6, time.Now())
	}()

	if !d.WaitForLock(2 * time.Second) {
		t.Fatal("WaitForLock did not release after the grandmaster was acquired")
	}
	if shift, _ := d.Overlay(); shift != -1_785_713_638_806_000_000 {
		t.Errorf("shift %d, want the acquired value stepped in directly", shift)
	}
}

func TestDisciplineStepsOnLargeError(t *testing.T) {
	d := &ClockDiscipline{
		staticNs:   665 * 1e6,
		stepNs:     5 * 1e6,
		maxSlewPPM: 500,
		lastTick:   time.Now().Add(-100 * time.Millisecond),
		acquired:   true,
		shiftNs:    665 * 1e6,
		acquiredCh: make(chan struct{}),
		stopChan:   make(chan struct{}),
	}
	target := d.shiftNs + 40*1e6
	d.applyEstimate(target, 0, time.Now())
	if d.shiftNs != target {
		t.Errorf("shift %d, want an immediate step to %d", d.shiftNs, target)
	}
}
