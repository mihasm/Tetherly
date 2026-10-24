package helper

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mihasm/tetherly/internal/ipc"
	"github.com/mihasm/tetherly/internal/tether"
	"github.com/mihasm/tetherly/internal/usb"
)

type fakeSession struct {
	phone usb.DeviceInfo
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	err   error
	info  *tether.Info
}

func (s *fakeSession) Phone() usb.DeviceInfo { return s.phone }
func (s *fakeSession) Done() <-chan struct{} { return s.done }
func (s *fakeSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *fakeSession) Info() *tether.Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}
func (s *fakeSession) Stop()                   { s.once.Do(func() { close(s.done) }) }
func (s *fakeSession) Interface() string       { return "utun9" }
func (s *fakeSession) Stats() (uint64, uint64) { return 0, 0 }

// die ends the session the way a relay error does.
func (s *fakeSession) die(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.Stop()
}

// bench is a helper with a fake USB bus and fake sessions.
type bench struct {
	t        *testing.T
	h        *Helper
	mu       sync.Mutex
	phones   []usb.DeviceInfo
	sessions []*fakeSession
	startErr error
	gate     chan struct{} // when set, start waits for it
}

func newBench(t *testing.T, phones ...usb.DeviceInfo) *bench {
	b := &bench{t: t, phones: phones}
	b.h = &Helper{cfg: ipc.DefaultConfig(), manualOff: map[string]bool{}, lastClient: time.Now()}
	b.h.discover = func() ([]usb.DeviceInfo, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return append([]usb.DeviceInfo(nil), b.phones...), nil
	}
	b.h.start = func(info usb.DeviceInfo, _ tether.Params) (tetherSession, error) {
		b.mu.Lock()
		gate, err := b.gate, b.startErr
		b.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if err != nil {
			return nil, err
		}
		s := &fakeSession{phone: info, done: make(chan struct{}), info: &tether.Info{IP: "10.0.0.2"}}
		b.mu.Lock()
		b.sessions = append(b.sessions, s)
		b.mu.Unlock()
		return s, nil
	}
	return b
}

func (b *bench) bus(phones ...usb.DeviceInfo) {
	b.mu.Lock()
	b.phones = phones
	b.mu.Unlock()
}

func (b *bench) started() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

func (b *bench) last() *fakeSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[len(b.sessions)-1]
}

// connected waits until n sessions have been started and the helper holds the last one.
func (b *bench) connected(n int) {
	b.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.h.mu.Lock()
		ok := b.h.sess != nil && !b.h.connecting
		b.h.mu.Unlock()
		if ok && b.started() == n {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("not connected with session %d (started %d)", n, b.started())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle runs a tick and lets a connect it started finish.
func (b *bench) tick() {
	b.h.tick()
	time.Sleep(20 * time.Millisecond)
	for {
		b.h.mu.Lock()
		c := b.h.connecting
		b.h.mu.Unlock()
		if !c {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (b *bench) noReconnectFor(n int) {
	b.t.Helper()
	if got := b.started(); got != n {
		b.t.Fatalf("%d sessions started, want %d", got, n)
	}
}

var (
	s22      = usb.DeviceInfo{VendorID: 0x04e8, ProductID: 0x6864, VIDPID: "04e8:6864", Product: "SAMSUNG_Android", Serial: "R5CT", HasRNDIS: true, HasADB: true}
	s22Plain = usb.DeviceInfo{VendorID: 0x04e8, ProductID: 0x6860, VIDPID: "04e8:6860", Product: "SAMSUNG_Android", Serial: "R5CT", HasADB: true}
	other    = usb.DeviceInfo{VendorID: 0x04e8, ProductID: 0x6864, VIDPID: "04e8:6864", Product: "SAMSUNG_Android", Serial: "OTHER", HasRNDIS: true}
)

func (b *bench) connect() {
	b.t.Helper()
	b.h.tick()
	if err := b.h.connect(); err != nil {
		b.t.Fatalf("connect: %v", err)
	}
	b.connected(b.started())
}

// The 2026-10-23 drops: the relay ends with the phone still tethering.
func TestSessionThatDiesIsStartedAgain(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.h.sessStart = time.Now().Add(-10 * time.Minute) // it had been running
	b.last().die(errors.New("relay: USB IN 0x81 keeps stalling after 5 halt clears: libusb: pipe error [code -9]"))
	b.tick()
	b.connected(2)
}

func TestShortSessionsBackOff(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.last().die(errors.New("relay: boom"))
	b.tick() // first end of a fresh session: at once
	b.connected(2)
	b.last().die(errors.New("relay: boom"))
	b.tick()
	b.noReconnectFor(2)
	b.h.mu.Lock()
	wait := time.Until(b.h.nextAuto)
	b.h.mu.Unlock()
	if wait < 4*time.Second || wait > 5*time.Second {
		t.Fatalf("second quick end waits %v, want 5 s", wait)
	}
	if st := b.h.Status(); st.State != ipc.StateConnecting || st.Hint != "Reconnecting" || st.Error != "relay: boom" {
		t.Fatalf("status between sessions = %s / %q / %q", st.State, st.Hint, st.Error)
	}
}

func TestFailedReconnectKeepsTrying(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.h.sessStart = time.Now().Add(-10 * time.Minute)
	b.mu.Lock()
	b.startErr = errors.New("usb: cannot claim RNDIS control interface 0: libusb: access denied")
	b.mu.Unlock()
	b.last().die(errors.New("relay: boom"))
	b.tick()
	b.h.mu.Lock()
	want, wait := b.h.want != nil, time.Until(b.h.nextAuto)
	b.h.mu.Unlock()
	if !want {
		t.Fatal("a failed reconnect dropped the wish")
	}
	if wait < 4*time.Second {
		t.Fatalf("after a failed attempt the next waits %v, want the backoff", wait)
	}
	b.mu.Lock()
	b.startErr = nil
	b.mu.Unlock()
	b.h.mu.Lock()
	b.h.nextAuto = time.Time{}
	b.h.mu.Unlock()
	b.tick()
	b.connected(2)
}

// 22:36:04: 6864 left, 6860 for a moment, 6864 again with the same serial.
func TestReenumeratedPhoneIsReconnected(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.h.sessStart = time.Now().Add(-10 * time.Minute)
	b.bus()
	b.tick() // the session's phone left: session ended
	if b.last().Err() != nil || b.h.sess != nil {
		t.Fatal("session should have been stopped")
	}
	if st := b.h.Status(); st.State != ipc.StateConnecting || st.Hint != "Waiting for the phone to come back" {
		t.Fatalf("status while gone = %s / %q", st.State, st.Hint)
	}
	b.bus(s22Plain)
	b.tick()
	b.noReconnectFor(1)
	b.bus(s22)
	b.tick()
	b.connected(2)
}

func TestAnotherPhoneIsNotTakenForTheWantedOne(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.bus(other)
	b.tick()
	b.tick()
	b.noReconnectFor(1)
	b.h.mu.Lock()
	ok := b.h.want != nil
	b.h.mu.Unlock()
	if !ok {
		t.Fatal("the wish for the S22 was dropped when another phone appeared")
	}
}

func TestTetheringSwitchedOffDropsTheWish(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.bus(s22Plain)
	b.tick() // session ended (phone changed profile)
	b.tick()
	b.h.mu.Lock()
	b.h.want.noTether = time.Now().Add(-tetherOffGrace - time.Second)
	b.h.mu.Unlock()
	b.tick()
	b.h.mu.Lock()
	gone := b.h.want == nil
	b.h.mu.Unlock()
	if !gone {
		t.Fatal("wish kept after tethering stayed off")
	}
	if !b.h.idle(0) {
		t.Fatal("helper should be idle once the wish is gone")
	}
	b.bus(s22)
	b.tick()
	b.noReconnectFor(1)
}

func TestPhoneGoneTooLongDropsTheWish(t *testing.T) {
	w := &wanted{phone: s22}
	now := time.Now()
	if st, _, _ := w.step(now, nil); st != wishWait {
		t.Fatalf("just gone: %v, want wait", st)
	}
	if st, _, why := w.step(now.Add(comebackWindow+time.Second), nil); st != wishDrop || why == "" {
		t.Fatalf("gone too long: %v %q, want drop", st, why)
	}
	w = &wanted{phone: s22}
	w.step(now, nil)
	if st, p, _ := w.step(now.Add(comebackWindow-time.Second), []usb.DeviceInfo{s22}); st != wishReconnect || p == nil || p.Serial != "R5CT" {
		t.Fatalf("back in time: %v %v, want reconnect", st, p)
	}
}

func TestDisconnectClearsTheWish(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.h.disconnect()
	if b.h.idle(0) != true {
		t.Fatal("helper not idle after disconnect")
	}
	b.tick()
	b.tick()
	b.noReconnectFor(1)
	if st := b.h.Status(); st.State != ipc.StateAvailable {
		t.Fatalf("state after disconnect = %s", st.State)
	}
}

func TestDisconnectDuringReconnectStopsIt(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.h.sessStart = time.Now().Add(-10 * time.Minute)
	gate := make(chan struct{})
	b.mu.Lock()
	b.gate = gate
	b.mu.Unlock()
	b.last().die(errors.New("relay: boom"))
	b.h.tick() // reconnect starts and waits at the gate
	deadline := time.Now().Add(time.Second)
	for {
		b.h.mu.Lock()
		c := b.h.connecting
		b.h.mu.Unlock()
		if c {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconnect did not start")
		}
		time.Sleep(2 * time.Millisecond)
	}
	b.h.disconnect()
	close(gate)
	b.tick()
	if b.started() != 2 {
		t.Fatalf("started %d", b.started())
	}
	select {
	case <-b.last().Done():
	default:
		t.Fatal("the session that came up after disconnect was left running")
	}
	b.h.mu.Lock()
	defer b.h.mu.Unlock()
	if b.h.sess != nil || b.h.want != nil {
		t.Fatal("disconnect during a reconnect left a session or a wish")
	}
}

func TestIdleWaitsForAWantedConnection(t *testing.T) {
	b := newBench(t, s22)
	b.connect()
	b.bus()
	b.tick()
	b.h.mu.Lock()
	b.h.lastClient = time.Now().Add(-time.Hour)
	b.h.mu.Unlock()
	if b.h.idle(90 * time.Second) {
		t.Fatal("helper would exit while a reconnect is pending")
	}
	b.h.disconnect()
	b.h.mu.Lock()
	b.h.lastClient = time.Now().Add(-time.Hour)
	b.h.mu.Unlock()
	if !b.h.idle(90 * time.Second) {
		t.Fatal("helper should exit when nothing is wanted")
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
	for n, d := range want {
		if got := backoff(n); got != d {
			t.Fatalf("backoff(%d) = %v, want %v", n, got, d)
		}
	}
}
