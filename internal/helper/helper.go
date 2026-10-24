// Package helper is the privileged side of Tetherly: a small root daemon that
// watches for Android phones, connects USB tethering when asked, and answers
// clients on a Unix socket.
//
// It never opens a USB device on its own unless auto-connect is switched on
// and the phone itself offers USB tethering. Watching is done by reading the
// I/O Registry, which no other client of the phone (adb, Android File
// Transfer, Xcode) can notice.
package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/mihasm/tetherly/internal/ipc"
	"github.com/mihasm/tetherly/internal/netcfg"
	"github.com/mihasm/tetherly/internal/tether"
	"github.com/mihasm/tetherly/internal/usb"
)

// ConfigFile holds the persistent configuration.
var ConfigFile = "/Library/Application Support/Tetherly/config.json"

const dhcpTimeout = 25 * time.Second

// tetherSession is what the helper needs of a tether.Session.
type tetherSession interface {
	Phone() usb.DeviceInfo
	Done() <-chan struct{}
	Err() error
	Info() *tether.Info
	Stop()
	Interface() string
	Stats() (tx, rx uint64)
}

func startTether(info usb.DeviceInfo, p tether.Params) (tetherSession, error) {
	s, err := tether.Start(info, p, nil)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Helper is the daemon state. All fields are guarded by mu.
type Helper struct {
	version  string
	discover func() ([]usb.DeviceInfo, error)
	start    func(usb.DeviceInfo, tether.Params) (tetherSession, error)

	mu         sync.Mutex
	cfg        ipc.Config
	phone      *usb.DeviceInfo
	sess       tetherSession
	sessStart  time.Time
	want       *wanted // a connection the user asked for; nil after disconnect
	gen        int     // counts disconnects, so a connect that was overtaken by one gives up
	connecting bool
	lastErr    string
	manualOff  map[string]bool
	failures   int
	nextAuto   time.Time
	lastClient time.Time
}

// New loads the configuration and returns a helper.
func New(version string) *Helper {
	h := &Helper{version: version, discover: usb.Discover, start: startTether, cfg: loadConfig(), manualOff: map[string]bool{}, lastClient: time.Now()}
	return h
}

func loadConfig() ipc.Config {
	cfg := ipc.DefaultConfig()
	data, err := os.ReadFile(ConfigFile)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Warn().Err(err).Str("file", ConfigFile).Msg("Config unreadable, using defaults")
			cfg = ipc.DefaultConfig()
		}
	}
	return sanitize(cfg)
}

func sanitize(c ipc.Config) ipc.Config {
	if c.MTU < 576 || c.MTU > 1500 {
		c.MTU = 1380
	}
	var dns []string
	for _, s := range c.DNSServers {
		if s = strings.TrimSpace(s); s != "" {
			dns = append(dns, s)
		}
	}
	c.DNSServers = dns
	return c
}

func (h *Helper) saveConfig() error {
	data, _ := json.MarshalIndent(h.cfg, "", "  ")
	if err := os.MkdirAll(filepath.Dir(ConfigFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ConfigFile, append(data, '\n'), 0o644)
}

func key(d *usb.DeviceInfo) string {
	if d == nil {
		return ""
	}
	return d.VIDPID + "/" + d.Serial
}

// Run watches the USB bus until stop is closed.
func (h *Helper) Run(stop <-chan struct{}, idleExit time.Duration) {
	netcfg.CleanupStale()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	h.tick()
	for {
		select {
		case <-stop:
			h.shutdown()
			return
		case <-t.C:
			h.tick()
			if idleExit > 0 && h.idle(idleExit) {
				log.Info().Msg("Idle (not connected, auto-connect off, no clients): exiting until launchd starts us again")
				h.shutdown()
				return
			}
		}
	}
}

func (h *Helper) idle(after time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sess == nil && !h.connecting && h.want == nil && !h.cfg.AutoConnect && time.Since(h.lastClient) > after
}

func (h *Helper) shutdown() {
	h.mu.Lock()
	s := h.sess
	h.sess = nil
	h.mu.Unlock()
	if s != nil {
		s.Stop()
	}
}

func (h *Helper) tick() {
	phones, err := h.discover()
	if err != nil {
		log.Warn().Err(err).Msg("USB discovery failed")
		return
	}
	h.mu.Lock()
	var cur *usb.DeviceInfo
	if len(phones) > 0 {
		cur = &phones[0]
	}
	// a wanted connection waits for its own phone
	if h.want != nil {
		for i := range phones {
			if samePhone(&h.want.phone, &phones[i]) {
				cur = &phones[i]
				break
			}
		}
	}
	// a session keeps its own phone while it is still on the bus
	if h.sess != nil {
		p := h.sess.Phone()
		k := key(&p)
		cur = nil
		for i := range phones {
			if key(&phones[i]) == k {
				cur = &phones[i]
			}
		}
	}
	prevKey, curKey := key(h.phone), key(cur)
	if prevKey != curKey && h.phone != nil {
		delete(h.manualOff, prevKey)
	}
	if cur == nil || !cur.HasRNDIS {
		delete(h.manualOff, curKey)
	}
	if prevKey != curKey {
		h.lastErr = ""
		if cur != nil {
			log.Info().Str("phone", cur.Product).Str("id", cur.VIDPID).Bool("usb_tethering", cur.HasRNDIS).Bool("adb", cur.HasADB).Msg("Phone detected")
		} else if h.phone != nil {
			log.Info().Msg("Phone unplugged")
		}
		h.failures = 0
	} else if cur != nil && h.phone != nil && cur.HasRNDIS != h.phone.HasRNDIS {
		log.Info().Bool("usb_tethering", cur.HasRNDIS).Msg("Phone switched USB tethering")
		h.lastErr = ""
		h.failures = 0
		h.nextAuto = time.Time{}
	}
	h.phone = cur

	sess := h.sess
	if sess != nil {
		select {
		case <-sess.Done():
			h.sess = nil
			// a session that died because the phone left or stopped tethering is a
			// normal end, not an error
			err := sess.Err()
			if err != nil && cur != nil && cur.HasRNDIS {
				h.setErr(err.Error())
			}
			reason := "session ended"
			if err != nil {
				reason = err.Error()
			}
			h.sessionEnded(reason)
			sess = nil
		default:
			if h.failures > 0 && sess.Info() != nil && time.Since(h.sessStart) > stableSession {
				h.failures = 0
			}
		}
	}
	h.mu.Unlock()

	if sess != nil {
		switch {
		case cur == nil:
			log.Info().Msg("Phone unplugged: disconnecting")
			h.endSession(sess, "", "the phone left the USB bus")
		case !cur.HasRNDIS:
			log.Info().Msg("USB tethering switched off on the phone: disconnecting")
			h.endSession(sess, "", "the phone stopped offering USB tethering")
		case sess.Info() == nil && time.Since(h.sessStartTime()) > dhcpTimeout:
			msg := "the phone gave no IP address (no DHCP answer) - is mobile data on?"
			h.endSession(sess, msg, msg)
		}
		return
	}

	h.mu.Lock()
	ready := cur != nil && cur.HasRNDIS && !h.connecting && time.Now().After(h.nextAuto)
	var reconnect *wanted
	if h.want != nil && !h.connecting {
		switch step, _, why := h.want.step(time.Now(), phones); step {
		case wishDrop:
			log.Info().Str("phone", h.want.phone.Product).Str("reason", why).Str("last_end", h.want.reason).Msg("Not reconnecting")
			h.want = nil
		case wishReconnect:
			if ready {
				w := *h.want
				reconnect = &w
			}
		}
	}
	auto := h.want == nil && ready && h.cfg.AutoConnect && !h.manualOff[curKey]
	attempt := h.failures + 1
	h.mu.Unlock()
	switch {
	case reconnect != nil:
		l := log.Info().Str("phone", cur.Product).Str("id", cur.VIDPID).Str("after", reconnect.reason).Int("attempt", attempt)
		if !reconnect.ended.IsZero() {
			l = l.Dur("down", time.Since(reconnect.ended).Round(100*time.Millisecond))
		}
		l.Msg("Reconnecting")
		go h.connect()
	case auto:
		log.Info().Msg("Auto-connect: the phone offers USB tethering")
		go h.connect()
	}
}

// sessionEnded records the end of a session the user did not stop (h.mu
// held): the wish stays, and the next attempt waits for the backoff unless
// the session had been running steadily.
func (h *Helper) sessionEnded(reason string) {
	if h.want == nil {
		return
	}
	h.want.ended, h.want.reason = time.Now(), reason
	if time.Since(h.sessStart) > stableSession {
		h.failures = 0
	}
	h.nextAuto = time.Now().Add(backoff(h.failures))
	h.failures++
}

func (h *Helper) sessStartTime() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessStart
}

func (h *Helper) setErr(msg string) {
	h.lastErr = msg
	if msg != "" {
		log.Error().Msg(msg)
	}
}

// endSession stops s. reason, when not empty, is why the helper ended it
// itself: a wanted connection is then started again; a disconnect passes "".
func (h *Helper) endSession(s tetherSession, errMsg, reason string) {
	s.Stop()
	h.mu.Lock()
	if h.sess == s {
		h.sess = nil
		if reason != "" {
			h.sessionEnded(reason)
		}
	}
	if errMsg != "" {
		h.setErr(errMsg)
	}
	h.mu.Unlock()
}

// connect starts a session with the current phone. Blocks until the session
// runs (or failed); DHCP finishes in the background.
func (h *Helper) connect() error {
	h.mu.Lock()
	if h.sess != nil || h.connecting {
		h.mu.Unlock()
		return nil
	}
	phone := h.phone
	if phone == nil {
		h.mu.Unlock()
		return errors.New("no Android phone is connected over USB")
	}
	if !phone.HasRNDIS {
		h.mu.Unlock()
		return errors.New("USB tethering is off on the phone: switch it on in Settings > Connections > Mobile Hotspot and Tethering > USB tethering")
	}
	h.connecting = true
	h.lastErr = ""
	delete(h.manualOff, key(phone))
	cfg := h.cfg
	info := *phone
	gen := h.gen
	h.mu.Unlock()

	log.Info().Str("phone", info.Product).Msg("Connecting")
	s, err := h.start(info, tether.Params{MTU: cfg.MTU, RouteAll: cfg.RouteAll, ManageDNS: cfg.ManageDNS, DNSServers: cfg.DNSServers})

	h.mu.Lock()
	h.connecting = false
	if err != nil {
		h.failures++
		h.nextAuto = time.Now().Add(backoff(h.failures))
		h.setErr(fmt.Sprintf("connect failed: %v", err))
		h.mu.Unlock()
		return err
	}
	if gen != h.gen {
		// disconnect came while this was starting
		h.mu.Unlock()
		s.Stop()
		return errors.New("disconnected while connecting")
	}
	h.sess = s
	h.sessStart = time.Now()
	h.want = &wanted{phone: info}
	h.mu.Unlock()
	return nil
}

func (h *Helper) disconnect() {
	h.mu.Lock()
	s := h.sess
	if h.phone != nil {
		h.manualOff[key(h.phone)] = true
	}
	h.lastErr = ""
	pending := h.want != nil
	h.want = nil
	h.gen++
	h.mu.Unlock()
	if s != nil {
		log.Info().Msg("Disconnect requested")
		h.endSession(s, "", "")
	} else if pending {
		log.Info().Msg("Disconnect requested: reconnecting stopped")
	}
}

// Status reports the current state.
func (h *Helper) Status() ipc.Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := ipc.Status{Version: h.version, AutoConnect: h.cfg.AutoConnect, MTU: h.cfg.MTU}
	if p := h.phone; p != nil {
		st.Phone = &ipc.Phone{Vendor: p.Vendor, Product: p.Product, Serial: p.Serial, VIDPID: p.VIDPID, RNDIS: p.HasRNDIS, ADB: p.HasADB}
	}
	switch {
	case h.sess != nil:
		st.Interface = h.sess.Interface()
		st.TxBytes, st.RxBytes = h.sess.Stats()
		if info := h.sess.Info(); info != nil {
			st.State = ipc.StateConnected
			st.IP, st.Gateway, st.DNS, st.DNSManaged, st.Routed, st.MTU = info.IP, info.Gateway, info.DNS, info.DNSManaged, info.Routed, info.MTU
			st.Since = info.Since.Format(time.RFC3339)
		} else {
			st.State = ipc.StateConnecting
		}
	case h.connecting:
		st.State = ipc.StateConnecting
	case h.want != nil:
		// a wanted connection between sessions: Disconnect stops it
		st.State = ipc.StateConnecting
		st.Error = h.lastErr
		switch {
		case h.phone == nil || !samePhone(&h.want.phone, h.phone):
			st.Hint = "Waiting for the phone to come back"
		case !h.phone.HasRNDIS:
			st.Hint = "Waiting for USB tethering on the phone"
		default:
			st.Hint = "Reconnecting"
		}
	case h.lastErr != "":
		st.State = ipc.StateError
		st.Error = h.lastErr
	case h.phone == nil:
		st.State = ipc.StateNoPhone
	case !h.phone.HasRNDIS:
		st.State = ipc.StatePhoneFound
		st.Hint = "Switch on USB tethering on the phone"
	default:
		st.State = ipc.StateAvailable
	}
	if st.State == ipc.StateError && h.phone != nil && !h.phone.HasRNDIS {
		st.Hint = "Switch on USB tethering on the phone"
	}
	return st
}

// Handle answers one request. privileged says whether the peer may change things.
func (h *Helper) Handle(req ipc.Request, privileged bool) ipc.Response {
	h.mu.Lock()
	h.lastClient = time.Now()
	h.mu.Unlock()
	deny := ipc.Response{Error: "not allowed: only an administrator or the logged-in user can change tethering"}
	switch req.Cmd {
	case "status", "":
		st := h.Status()
		return ipc.Response{OK: true, Status: &st}
	case "version":
		st := ipc.Status{Version: h.version}
		return ipc.Response{OK: true, Status: &st}
	case "logs":
		return ipc.Response{OK: true, Logs: logs.since(req.Since)}
	case "get_config":
		h.mu.Lock()
		c := h.cfg
		h.mu.Unlock()
		return ipc.Response{OK: true, Config: &c}
	case "connect":
		if !privileged {
			return deny
		}
		err := h.connect()
		st := h.Status()
		if err != nil {
			return ipc.Response{Error: err.Error(), Status: &st}
		}
		return ipc.Response{OK: true, Status: &st}
	case "disconnect":
		if !privileged {
			return deny
		}
		h.disconnect()
		st := h.Status()
		return ipc.Response{OK: true, Status: &st}
	case "set_config":
		if !privileged {
			return deny
		}
		if req.Config == nil {
			return ipc.Response{Error: "set_config needs a config"}
		}
		h.mu.Lock()
		h.cfg = sanitize(*req.Config)
		err := h.saveConfig()
		c := h.cfg
		if c.AutoConnect {
			h.nextAuto = time.Time{}
		}
		h.mu.Unlock()
		log.Info().Bool("auto_connect", c.AutoConnect).Int("mtu", c.MTU).Bool("route_all", c.RouteAll).Bool("manage_dns", c.ManageDNS).Strs("dns", c.DNSServers).Msg("Settings changed (MTU/route/DNS apply from the next connect)")
		if err != nil {
			return ipc.Response{Error: "saved in memory, but not on disk: " + err.Error(), Config: &c}
		}
		return ipc.Response{OK: true, Config: &c}
	}
	return ipc.Response{Error: "unknown command: " + req.Cmd}
}
