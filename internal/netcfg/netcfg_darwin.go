// Package netcfg applies and removes the routes and DNS of a tether session.
//
// Routing: two /1 routes (0.0.0.0/1 and 128.0.0.0/1) through the utun
// interface win over the existing default route without deleting it, while
// every directly connected subnet stays where it is. With the Mac's Wi-Fi on
// an OBD adapter or a router without internet (say 192.168.0.0/24), that
// subnet stays on Wi-Fi and everything else goes through the phone.
//
// DNS and reachability: the session is published to configd as a network
// service (State:/Network/Service/<id>/IPv4 + DNS) with OverridePrimary, the
// way VPN clients do it. macOS then treats the phone link as the primary
// service: its DNS servers are used for all lookups and Safari, App Store and
// other SystemConfiguration/Network.framework clients see the Mac as online.
// Nothing is written to the persistent network preferences, so a crash or a
// reboot cannot leave DNS changed behind.
package netcfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// ServiceID is the configd service key used for the tether link.
const ServiceID = "dev.miha.tetherly"

// StateFile records what is applied, so a restarted helper can undo it.
var StateFile = "/var/run/tetherly.state"

// Applied is what one session put in place.
type Applied struct {
	Interface string   `json:"interface"`
	IP        string   `json:"ip"`
	Gateway   string   `json:"gateway"`
	Routes    bool     `json:"routes"`
	Service   bool     `json:"service"`
	DNS       []string `json:"dns"`
}

// Options says what to apply.
type Options struct {
	RouteAll  bool     // send all traffic through the phone
	ManageDNS bool     // publish the service with DNS and OverridePrimary
	DNS       []string // servers, in order
}

// Apply installs routes and the configd service for an interface that is
// already configured with ip -> gateway.
func Apply(ifname, ip, gateway string, o Options) (*Applied, error) {
	a := &Applied{Interface: ifname, IP: ip, Gateway: gateway}
	var errs []string
	if o.RouteAll {
		ok := true
		for _, net := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if out, err := exec.Command("/sbin/route", "-n", "add", "-net", net, "-interface", ifname).CombinedOutput(); err != nil {
				ok = false
				errs = append(errs, fmt.Sprintf("route add %s: %v (%s)", net, err, strings.TrimSpace(string(out))))
			}
		}
		a.Routes = ok
	}
	if o.ManageDNS && o.RouteAll && len(o.DNS) > 0 {
		var b strings.Builder
		b.WriteString("d.init\n")
		fmt.Fprintf(&b, "d.add Addresses * %s\n", ip)
		fmt.Fprintf(&b, "d.add DestAddresses * %s\n", gateway)
		fmt.Fprintf(&b, "d.add SubnetMasks * 255.255.255.255\n")
		fmt.Fprintf(&b, "d.add InterfaceName %s\n", ifname)
		fmt.Fprintf(&b, "d.add Router %s\n", gateway)
		b.WriteString("d.add OverridePrimary # 1\n")
		fmt.Fprintf(&b, "set State:/Network/Service/%s/IPv4\n", ServiceID)
		b.WriteString("d.init\n")
		fmt.Fprintf(&b, "d.add ServerAddresses * %s\n", strings.Join(o.DNS, " "))
		fmt.Fprintf(&b, "set State:/Network/Service/%s/DNS\n", ServiceID)
		if err := scutil(b.String()); err != nil {
			errs = append(errs, err.Error())
		} else {
			a.Service = true
			a.DNS = o.DNS
		}
	}
	saveState(a)
	if len(errs) > 0 {
		return a, fmt.Errorf("netcfg: %s", strings.Join(errs, "; "))
	}
	log.Info().Str("component", "netcfg").Str("interface", ifname).Bool("routes", a.Routes).Strs("dns", a.DNS).Msg("Network configured")
	return a, nil
}

// Remove undoes what Apply did. Safe to call twice.
func (a *Applied) Remove() {
	if a == nil {
		return
	}
	if a.Service {
		_ = scutil(fmt.Sprintf("remove State:/Network/Service/%s/IPv4\nremove State:/Network/Service/%s/DNS\n", ServiceID, ServiceID))
		a.Service = false
	}
	if a.Routes {
		for _, net := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if out, err := exec.Command("/sbin/route", "-n", "delete", "-net", net, "-interface", a.Interface).CombinedOutput(); err != nil {
				log.Debug().Str("component", "netcfg").Str("route", net).Str("out", strings.TrimSpace(string(out))).Msg("route delete")
			}
		}
		a.Routes = false
	}
	_ = os.Remove(StateFile)
	log.Info().Str("component", "netcfg").Str("interface", a.Interface).Msg("Network configuration removed")
}

// CleanupStale removes what a previous helper process left behind (crash,
// kill -9). Only our own service key and routes on our recorded interface are
// touched; other VPNs' routes are left alone.
func CleanupStale() {
	data, err := os.ReadFile(StateFile)
	if err == nil {
		var a Applied
		if json.Unmarshal(data, &a) == nil && a.Interface != "" {
			log.Info().Str("component", "netcfg").Str("interface", a.Interface).Msg("Removing configuration left by a previous run")
			a.Service, a.Routes = true, true
			a.Remove()
			return
		}
	}
	_ = scutil(fmt.Sprintf("remove State:/Network/Service/%s/IPv4\nremove State:/Network/Service/%s/DNS\n", ServiceID, ServiceID))
}

func saveState(a *Applied) {
	data, _ := json.Marshal(a)
	_ = os.MkdirAll(filepath.Dir(StateFile), 0o755)
	if err := os.WriteFile(StateFile, data, 0o644); err != nil {
		log.Warn().Str("component", "netcfg").Err(err).Msg("Cannot write state file")
	}
}

func scutil(script string) error {
	cmd := exec.Command("/usr/sbin/scutil")
	cmd.Stdin = strings.NewReader("open\n" + script + "close\nquit\n")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scutil: %v (%s)", err, strings.TrimSpace(out.String()))
	}
	if s := strings.TrimSpace(out.String()); s != "" {
		log.Debug().Str("component", "netcfg").Str("scutil", s).Msg("scutil output")
	}
	return nil
}
