// tetherly-helper is Tetherly's privileged helper and its command-line client.
//
//	tetherly-helper daemon            run the root daemon (launchd starts it)
//	tetherly status [--json]          show the state
//	tetherly connect | disconnect
//	tetherly logs [-f]
//	tetherly config [key=value ...]   show or change settings
//	tetherly version
//
// Invoked as `tetherly` (a symlink) it is the client; `daemon` is only for launchd.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/mihasm/tetherly/internal/helper"
	"github.com/mihasm/tetherly/internal/ipc"
	"github.com/mihasm/tetherly/internal/netcfg"
	"github.com/mihasm/tetherly/internal/usb"
)

// Injected at build time: -ldflags="-X main.version=..."
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "daemon":
		daemon(args[1:])
	case "status":
		status(args[1:])
	case "connect", "disconnect":
		simple(args[0])
	case "logs":
		logsCmd(args[1:])
	case "config":
		config(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("tetherly %s\n", version)
		if r, err := ipc.Call(ipc.Request{Cmd: "version"}, 2*time.Second); err == nil && r.Status != nil {
			fmt.Printf("helper   %s\n", r.Status.Version)
		}
	case "devices":
		devices()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: %s <command>

  status [--json]          connection state
  connect                  start USB tethering with the plugged-in phone
  disconnect               stop it and release the phone
  logs [-f]                helper log (-f follows)
  config [key=value ...]   show or change: auto_connect, mtu, route_all, manage_dns, dns_servers
  devices                  list USB devices and their interfaces (read-only, for bug reports)
  version
`, filepath.Base(os.Args[0]))
}

func daemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	debug := fs.Bool("debug", false, "debug logging")
	socket := fs.String("socket", "", "socket path (default "+ipc.SocketPath+")")
	idle := fs.Duration("idle-exit", 90*time.Second, "when socket-activated: exit after this long idle (0 = never)")
	fs.Parse(args)
	if *socket != "" {
		ipc.SocketPath = *socket
	}
	if os.Geteuid() != 0 {
		// development run: keep state out of system locations
		dir := filepath.Join(os.TempDir(), "tetherly-dev")
		helper.LogFile = filepath.Join(dir, "tetherly.log")
		helper.ConfigFile = filepath.Join(dir, "config.json")
		netcfg.StateFile = filepath.Join(dir, "tetherly.state")
		if *socket == "" {
			ipc.SocketPath = filepath.Join(dir, "tetherly.sock")
		}
		_ = os.MkdirAll(dir, 0o755)
	}
	helper.SetupLogging(*debug)
	log.Info().Str("version", version).Int("pid", os.Getpid()).Msg("Tetherly helper starting")

	l, activated, err := helper.Listen()
	if err != nil {
		log.Error().Err(err).Msg("Cannot listen")
		os.Exit(1)
	}
	h := helper.New(version)
	go h.Serve(l)

	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Info().Str("signal", s.String()).Msg("Stopping")
		close(stop)
	}()
	idleExit := time.Duration(0)
	if activated {
		idleExit = *idle
	}
	h.Run(stop, idleExit)
	l.Close()
	if !activated {
		_ = os.Remove(ipc.SocketPath)
	}
	log.Info().Msg("Helper stopped")
}

func call(req ipc.Request, timeout time.Duration) *ipc.Response {
	r, err := ipc.Call(req, timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tetherly:", err)
		fmt.Fprintln(os.Stderr, "Open Tetherly.app once to install its helper.")
		os.Exit(3)
	}
	return r
}

func status(args []string) {
	asJSON := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	r := call(ipc.Request{Cmd: "status"}, 3*time.Second)
	if asJSON {
		data, _ := json.MarshalIndent(r.Status, "", "  ")
		fmt.Println(string(data))
		return
	}
	printStatus(r.Status)
}

func printStatus(s *ipc.Status) {
	if s == nil {
		return
	}
	labels := map[string]string{
		ipc.StateNoPhone: "no phone", ipc.StatePhoneFound: "phone found, USB tethering off",
		ipc.StateAvailable: "ready to connect", ipc.StateConnecting: "connecting",
		ipc.StateConnected: "connected", ipc.StateDisconnecting: "disconnecting", ipc.StateError: "error",
	}
	fmt.Printf("state:      %s\n", labels[s.State])
	if s.Phone != nil {
		fmt.Printf("phone:      %s %s (%s)\n", s.Phone.Vendor, s.Phone.Product, s.Phone.VIDPID)
	}
	if s.State == ipc.StateConnected {
		fmt.Printf("interface:  %s  %s -> %s  mtu %d\n", s.Interface, s.IP, s.Gateway, s.MTU)
		fmt.Printf("dns:        %s (managed: %v)\n", strings.Join(s.DNS, ", "), s.DNSManaged)
		fmt.Printf("traffic:    %.1f MB down, %.1f MB up\n", float64(s.RxBytes)/1e6, float64(s.TxBytes)/1e6)
		fmt.Printf("since:      %s\n", s.Since)
	}
	if s.Error != "" {
		fmt.Printf("error:      %s\n", s.Error)
	}
	if s.Hint != "" {
		fmt.Printf("hint:       %s\n", s.Hint)
	}
	fmt.Printf("auto:       %v\n", s.AutoConnect)
}

func simple(cmd string) {
	r := call(ipc.Request{Cmd: cmd}, 20*time.Second)
	if !r.OK {
		fmt.Fprintln(os.Stderr, "tetherly:", r.Error)
		os.Exit(1)
	}
	if cmd == "connect" {
		// wait for DHCP
		for i := 0; i < 30; i++ {
			r = call(ipc.Request{Cmd: "status"}, 3*time.Second)
			if r.Status.State != ipc.StateConnecting {
				break
			}
			time.Sleep(time.Second)
		}
	}
	printStatus(r.Status)
	if r.Status != nil && r.Status.State == ipc.StateError {
		os.Exit(1)
	}
}

func logsCmd(args []string) {
	follow := len(args) > 0 && args[0] == "-f"
	var since int64
	for {
		r := call(ipc.Request{Cmd: "logs", Since: since}, 3*time.Second)
		for _, l := range r.Logs {
			fmt.Printf("%s %s\n", l.Time, l.Text)
			since = l.N
		}
		if !follow {
			return
		}
		time.Sleep(time.Second)
	}
}

func config(args []string) {
	r := call(ipc.Request{Cmd: "get_config"}, 3*time.Second)
	c := *r.Config
	if len(args) > 0 {
		for _, a := range args {
			k, v, ok := strings.Cut(a, "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "expected key=value, got %q\n", a)
				os.Exit(2)
			}
			var err error
			switch k {
			case "auto_connect":
				c.AutoConnect, err = strconv.ParseBool(v)
			case "route_all":
				c.RouteAll, err = strconv.ParseBool(v)
			case "manage_dns":
				c.ManageDNS, err = strconv.ParseBool(v)
			case "mtu":
				c.MTU, err = strconv.Atoi(v)
			case "dns_servers":
				c.DNSServers = nil
				if v != "" {
					c.DNSServers = strings.Split(v, ",")
				}
			default:
				err = fmt.Errorf("unknown key")
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", k, err)
				os.Exit(2)
			}
		}
		r = call(ipc.Request{Cmd: "set_config", Config: &c}, 3*time.Second)
		if !r.OK {
			fmt.Fprintln(os.Stderr, "tetherly:", r.Error)
			os.Exit(1)
		}
		c = *r.Config
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	fmt.Println(string(data))
}

func devices() {
	all, err := usb.ListAll()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, d := range all {
		tag := ""
		switch {
		case d.HasRNDIS:
			tag = "  [Android, USB tethering ON]"
		case d.IsAndroid():
			tag = "  [Android, USB tethering off]"
		}
		fmt.Printf("%s  %s %s  serial=%q location=0x%08x address=%d%s\n", d.VIDPID, d.Vendor, d.Product, d.Serial, d.Location, d.Address, tag)
		for _, i := range d.Interfaces {
			kind := ""
			switch {
			case usb.IsRNDISControl(uint8(i.Class), uint8(i.SubClass), uint8(i.Protocol)):
				kind = " RNDIS control"
			case uint8(i.Class) == usb.CDCDataClass:
				kind = " CDC data"
			case usb.IsADB(uint8(i.Class), uint8(i.SubClass), uint8(i.Protocol)):
				kind = " ADB (never touched)"
			case uint8(i.Class) == usb.ImageClass:
				kind = " MTP/PTP"
			}
			fmt.Printf("    interface %d  class %02x/%02x/%02x%s\n", i.Number, i.Class, i.SubClass, i.Protocol, kind)
		}
	}
}
