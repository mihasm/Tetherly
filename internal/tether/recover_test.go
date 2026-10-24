package tether

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/gousb"

	"github.com/mihasm/tetherly/internal/rndis"
)

// scriptIn returns the scripted results in order, then the last one forever
// (or blocks until the context ends when the script says so).
type scriptIn struct {
	mu    sync.Mutex
	steps []inStep
	reads int
}

type inStep struct {
	data  []byte
	err   error
	block bool
}

func (s *scriptIn) ReadContext(ctx context.Context, buf []byte) (int, error) {
	s.mu.Lock()
	st := s.steps[0]
	if len(s.steps) > 1 {
		s.steps = s.steps[1:]
	}
	s.reads++
	s.mu.Unlock()
	if st.block {
		<-ctx.Done()
		return 0, gousb.TransferCancelled
	}
	if st.err != nil {
		return 0, st.err
	}
	return copy(buf, st.data), nil
}

type fakeOut struct {
	mu     sync.Mutex
	fails  []error // returned by the first writes, one each
	writes [][]byte
}

func (o *fakeOut) WriteContext(ctx context.Context, buf []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.fails) > 0 {
		err := o.fails[0]
		o.fails = o.fails[1:]
		return 0, err
	}
	o.writes = append(o.writes, append([]byte(nil), buf...))
	return len(buf), nil
}

type fakeTun struct {
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	got    [][]byte
}

func newFakeTun() *fakeTun { return &fakeTun{closed: make(chan struct{})} }

func (t *fakeTun) Read(p []byte) (int, error) { <-t.closed; return 0, io.EOF }
func (t *fakeTun) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.got = append(t.got, append([]byte(nil), p...))
	t.mu.Unlock()
	return len(p), nil
}
func (t *fakeTun) Close() error                              { t.once.Do(func() { close(t.closed) }); return nil }
func (t *fakeTun) Name() string                              { return "utun-test" }
func (t *fakeTun) Configure(local, remote, mtu string) error { return nil }
func (t *fakeTun) packets() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.got)
}

type clearLog struct {
	mu    sync.Mutex
	addrs []uint8
	err   error
}

func (c *clearLog) clear(addr uint8) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addrs = append(c.addrs, addr)
	return c.err
}

func (c *clearLog) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.addrs)
}

// ipFrame is an RNDIS data message carrying one small IPv4 packet.
func ipFrame() []byte {
	eth := make([]byte, 14+40)
	copy(eth[0:6], []byte{0x02, 0, 0, 0, 0, 1})
	copy(eth[6:12], []byte{0x66, 0x9f, 0xe0, 0xaa, 0x9d, 0xe0})
	binary.BigEndian.PutUint16(eth[12:14], 0x0800)
	eth[14] = 0x45
	eth[14+9] = 6
	return rndis.EncapsulatePacket(eth)
}

func testRelay(in bulkIn, out bulkOut, cl *clearLog, tn *fakeTun) *Relay {
	r := newRelay(context.Background(), in, out, 0x81, 0x01, cl.clear, tn, []byte{0x66, 0x9f, 0xe0, 0xaa, 0x9d, 0xe0})
	r.inGuard.pause = func(time.Duration) {}
	r.outGuard.pause = func(time.Duration) {}
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The 2026-10-23 drops: a transaction error, then LIBUSB_ERROR_PIPE on every
// read. The relay must clear the halt and go on, not end the session.
func TestStalledInEndpointIsClearedAndReadingGoesOn(t *testing.T) {
	in := &scriptIn{steps: []inStep{
		{err: gousb.TransferError},
		{err: gousb.ErrorPipe},
		{err: gousb.ErrorPipe},
		{data: ipFrame()},
		{block: true},
	}}
	cl, tn := &clearLog{}, newFakeTun()
	r := testRelay(in, &fakeOut{}, cl, tn)
	done := make(chan error, 1)
	go func() { done <- r.Start() }()

	waitFor(t, "the packet after the stall", func() bool { return tn.packets() == 1 })
	if n := cl.count(); n != 2 {
		t.Fatalf("halt cleared %d times, want 2 (once per pipe error)", n)
	}
	for _, a := range cl.addrs {
		if a != 0x81 {
			t.Fatalf("cleared endpoint 0x%02x, want 0x81", a)
		}
	}
	if r.inGuard.errs != 0 || r.inGuard.clears != 0 {
		t.Fatalf("guard not reset after a good read: errs=%d clears=%d", r.inGuard.errs, r.inGuard.clears)
	}
	select {
	case err := <-done:
		t.Fatalf("relay ended after a recovered stall: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	r.Stop()
	tn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Start after Stop = %v, want nil", err)
	}
}

func TestEndpointThatKeepsStallingEndsTheSession(t *testing.T) {
	in := &scriptIn{steps: []inStep{{err: gousb.ErrorPipe}}}
	cl, tn := &clearLog{}, newFakeTun()
	r := testRelay(in, &fakeOut{}, cl, tn)
	defer tn.Close()
	err := r.Start()
	if err == nil || !strings.Contains(err.Error(), "IN 0x81 keeps stalling after 5 halt clears") {
		t.Fatalf("Start = %v, want the keeps-stalling error", err)
	}
	if !errors.Is(err, gousb.ErrorPipe) {
		t.Fatalf("error %v does not wrap the pipe error", err)
	}
	if n := cl.count(); n != maxClears {
		t.Fatalf("halt cleared %d times, want %d", n, maxClears)
	}
}

func TestPhoneGoneEndsTheSessionAtOnce(t *testing.T) {
	in := &scriptIn{steps: []inStep{{err: gousb.ErrorNoDevice}}}
	cl, tn := &clearLog{}, newFakeTun()
	r := testRelay(in, &fakeOut{}, cl, tn)
	defer tn.Close()
	err := r.Start()
	if err == nil || !strings.Contains(err.Error(), "phone went away") {
		t.Fatalf("Start = %v, want phone went away", err)
	}
	if cl.count() != 0 {
		t.Fatal("a halt was cleared on a phone that is gone")
	}
}

func TestClearThatFindsNoDeviceEndsTheSession(t *testing.T) {
	g := newGuard("IN", 0x81, func(uint8) error { return gousb.ErrorNoDevice }, func(time.Duration) {})
	if err := g.fail(gousb.ErrorPipe); err == nil || !strings.Contains(err.Error(), "phone went away") {
		t.Fatalf("fail = %v, want phone went away", err)
	}
}

func TestOtherErrorsAreRetriedAndClearedEveryFifth(t *testing.T) {
	cl := &clearLog{}
	var paused []time.Duration
	g := newGuard("IN", 0x81, cl.clear, func(d time.Duration) { paused = append(paused, d) })
	for i := 0; i < clearEvery-1; i++ {
		if err := g.fail(gousb.TransferError); err != nil {
			t.Fatalf("error %d ended the session: %v", i+1, err)
		}
	}
	if cl.count() != 0 {
		t.Fatalf("cleared after %d plain errors", clearEvery-1)
	}
	if err := g.fail(gousb.TransferError); err != nil {
		t.Fatal(err)
	}
	if cl.count() != 1 {
		t.Fatalf("cleared %d times after %d errors, want 1", cl.count(), clearEvery)
	}
	if paused[0] != retryPause || paused[len(paused)-1] != clearBackoff {
		t.Fatalf("pauses %v", paused)
	}
	g.ok()
	if g.errs != 0 || g.clears != 0 {
		t.Fatal("ok did not reset the guard")
	}
}

func TestClearBackoffGrowsAndGivesUp(t *testing.T) {
	var paused []time.Duration
	failing := errors.New("clear failed")
	g := newGuard("OUT", 0x01, func(uint8) error { return failing }, func(d time.Duration) { paused = append(paused, d) })
	var err error
	for i := 0; i <= maxClears && err == nil; i++ {
		err = g.fail(gousb.ErrorPipe)
	}
	if err == nil || !strings.Contains(err.Error(), "keeps failing after 5 halt clears (last clear: clear failed)") {
		t.Fatalf("fail = %v", err)
	}
	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond}
	if len(paused) != len(want) {
		t.Fatalf("pauses %v, want %v", paused, want)
	}
	for i := range want {
		if paused[i] != want[i] {
			t.Fatalf("pauses %v, want %v", paused, want)
		}
	}
}

func TestStalledOutEndpointIsClearedAndTheWriteRetried(t *testing.T) {
	out := &fakeOut{fails: []error{gousb.TransferStall}}
	cl, tn := &clearLog{}, newFakeTun()
	r := testRelay(&scriptIn{steps: []inStep{{block: true}}}, out, cl, tn)
	defer tn.Close()
	pkt := []byte{1, 2, 3}
	if err := r.send(pkt); err != nil {
		t.Fatalf("send = %v", err)
	}
	if cl.count() != 1 || cl.addrs[0] != 0x01 {
		t.Fatalf("clears %v, want [0x01]", cl.addrs)
	}
	if len(out.writes) != 1 || string(out.writes[0]) != string(pkt) {
		t.Fatalf("writes %v, want the packet once", out.writes)
	}
}

func TestNoClearAfterStop(t *testing.T) {
	cl, tn := &clearLog{}, newFakeTun()
	r := testRelay(&scriptIn{steps: []inStep{{block: true}}}, &fakeOut{}, cl, tn)
	defer tn.Close()
	r.Stop()
	if err := r.clearHalt(0x81); !errors.Is(err, context.Canceled) {
		t.Fatalf("clearHalt after Stop = %v", err)
	}
	if cl.count() != 0 {
		t.Fatal("cleared a halt after Stop: the device may already be closed")
	}
}
