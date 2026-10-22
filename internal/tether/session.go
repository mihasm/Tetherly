package tether

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/mihasm/tetherly/internal/netcfg"
	"github.com/mihasm/tetherly/internal/rndis"
	"github.com/mihasm/tetherly/internal/tun"
	"github.com/mihasm/tetherly/internal/usb"
)

// Params configure one session.
type Params struct {
	MTU        int
	RouteAll   bool
	ManageDNS  bool
	DNSServers []string // custom servers; empty = the phone's DNS, then 1.1.1.1 and 8.8.8.8
}

// Info is what a configured session looks like.
type Info struct {
	Interface  string
	IP         string
	Gateway    string
	DNS        []string
	DNSManaged bool
	Routed     bool
	MTU        int
	Since      time.Time
}

// Session is one tether connection: device claimed, RNDIS up, utun relaying.
type Session struct {
	Device usb.DeviceInfo

	params  Params
	dev     *usb.Device
	rn      *rndis.Session
	iface   tun.Interface
	relay   *Relay
	cancel  context.CancelFunc
	done    chan struct{}
	stopped sync.Once

	mu      sync.Mutex
	info    *Info
	applied *netcfg.Applied
	err     error

	// OnConfigured is called when DHCP finished and the network is applied.
	OnConfigured func(Info)
}

// Start claims the phone's RNDIS interfaces, does the RNDIS handshake,
// creates the utun interface and starts relaying. It returns once the relay
// runs; OnConfigured follows when the phone has given us an address.
func Start(info usb.DeviceInfo, p Params, onConfigured func(Info)) (*Session, error) {
	if p.MTU <= 0 {
		p.MTU = 1380
	}
	s := &Session{Device: info, params: p, done: make(chan struct{}), OnConfigured: onConfigured}

	dev, err := usb.Open(info)
	if err != nil {
		return nil, err
	}
	s.dev = dev
	log.Info().Str("component", "session").Str("device", info.VIDPID).Str("product", info.Product).Int("rndis_interface", dev.InterfaceNum).Msg("Phone opened, RNDIS interfaces claimed")

	s.rn = rndis.NewSession(dev)
	phoneMAC, err := s.rn.Handshake()
	if err != nil {
		dev.Close()
		return nil, err
	}

	iface, err := tun.OpenUTUN(0)
	if err != nil {
		s.rn.Halt()
		dev.Close()
		return nil, err
	}
	s.iface = iface

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	relay, err := NewRelay(ctx, dev, iface, phoneMAC)
	if err != nil {
		cancel()
		iface.Close()
		s.rn.Halt()
		dev.Close()
		return nil, err
	}
	s.relay = relay
	relay.OnDHCP = s.configure

	go func() {
		err := relay.Start()
		s.mu.Lock()
		if s.err == nil {
			s.err = err
		}
		s.mu.Unlock()
		s.Stop()
	}()
	log.Info().Str("component", "session").Str("interface", iface.Name()).Msg("Relay running, waiting for DHCP")
	return s, nil
}

func (s *Session) configure(gateway, client string, phoneDNS []string, lease time.Duration) {
	mtu := strconv.Itoa(s.params.MTU)
	if err := s.iface.Configure(client, gateway, mtu); err != nil {
		log.Error().Str("component", "session").Err(err).Msg("Cannot configure interface")
		s.fail(err)
		return
	}
	dns := s.params.DNSServers
	if len(dns) == 0 {
		dns = append(dns, phoneDNS...)
		if len(dns) == 0 {
			dns = append(dns, gateway)
		}
		dns = append(dns, "1.1.1.1", "8.8.8.8")
	}
	applied, err := netcfg.Apply(s.iface.Name(), client, gateway, netcfg.Options{RouteAll: s.params.RouteAll, ManageDNS: s.params.ManageDNS, DNS: dedupe(dns)})
	if err != nil {
		log.Warn().Str("component", "session").Err(err).Msg("Network partly configured")
	}
	info := Info{
		Interface:  s.iface.Name(),
		IP:         client,
		Gateway:    gateway,
		DNS:        applied.DNS,
		DNSManaged: applied.Service,
		Routed:     applied.Routes,
		MTU:        s.params.MTU,
		Since:      time.Now(),
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		applied.Remove()
		return
	default:
	}
	s.applied = applied
	s.info = &info
	s.mu.Unlock()
	log.Info().Str("component", "session").Str("ip", client).Str("gateway", gateway).Dur("lease", lease).Msg("Connected")
	if s.OnConfigured != nil {
		s.OnConfigured(info)
	}
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	go s.Stop()
}

// Stop tears everything down in reverse order and releases the phone:
// routes and DNS, relay, RNDIS HALT, utun, USB interfaces and handle.
func (s *Session) Stop() {
	s.stopped.Do(func() {
		s.mu.Lock()
		applied := s.applied
		s.applied = nil
		close(s.done)
		s.mu.Unlock()
		applied.Remove()
		if s.relay != nil {
			s.relay.Stop()
		}
		if s.rn != nil {
			if err := s.rn.Halt(); err != nil {
				log.Debug().Str("component", "session").Err(err).Msg("RNDIS halt")
			}
		}
		if s.iface != nil {
			s.iface.Close()
		}
		if s.dev != nil {
			s.dev.Close()
		}
		log.Info().Str("component", "session").Msg("Phone released")
	})
}

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended (nil after a requested stop).
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Info returns the configuration once DHCP finished, else nil.
func (s *Session) Info() *Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.info == nil {
		return nil
	}
	c := *s.info
	return &c
}

// Interface is the utun name.
func (s *Session) Interface() string {
	if s.iface == nil {
		return ""
	}
	return s.iface.Name()
}

// Stats returns bytes sent to / received from the phone.
func (s *Session) Stats() (tx, rx uint64) {
	if s.relay == nil {
		return 0, 0
	}
	return s.relay.Stats()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
