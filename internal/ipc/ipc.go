// Package ipc is the local protocol between the Tetherly helper (root) and
// its clients: the menu bar app, the `tetherly` CLI and other programs.
//
// Transport: the Unix socket /var/run/tetherly.sock. One JSON object per line
// in each direction; a client may send several requests on one connection.
//
//	-> {"cmd":"status"}
//	<- {"ok":true,"status":{"state":"connected","interface":"utun6",...}}
//
// Anyone may read status and logs. Commands that change something (connect,
// disconnect, set_config) need root, a member of the admin group, or the user
// logged in at the console.
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// SocketPath is where the helper listens (TETHERLY_SOCKET overrides it).
var SocketPath = "/var/run/tetherly.sock"

func init() {
	if p := os.Getenv("TETHERLY_SOCKET"); p != "" {
		SocketPath = p
	}
}

// States reported in Status.State.
const (
	StateNoPhone       = "no_phone"            // no Android phone on USB
	StatePhoneFound    = "phone_found"         // phone plugged in, USB tethering off
	StateAvailable     = "tethering_available" // RNDIS offered, not connected
	StateConnecting    = "connecting"
	StateConnected     = "connected"
	StateDisconnecting = "disconnecting"
	StateError         = "error"
)

// Phone identifies the plugged-in phone.
type Phone struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	Serial  string `json:"serial"`
	VIDPID  string `json:"vid_pid"`
	RNDIS   bool   `json:"rndis"`
	ADB     bool   `json:"adb"`
}

// Status is the helper's current state.
type Status struct {
	State       string   `json:"state"`
	Phone       *Phone   `json:"phone,omitempty"`
	Interface   string   `json:"interface,omitempty"`
	IP          string   `json:"ip,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	DNS         []string `json:"dns,omitempty"`
	DNSManaged  bool     `json:"dns_managed"`
	Routed      bool     `json:"routed"`
	MTU         int      `json:"mtu,omitempty"`
	Since       string   `json:"since,omitempty"`
	RxBytes     uint64   `json:"rx_bytes"`
	TxBytes     uint64   `json:"tx_bytes"`
	Error       string   `json:"error,omitempty"`
	Hint        string   `json:"hint,omitempty"`
	AutoConnect bool     `json:"auto_connect"`
	Version     string   `json:"version"`
}

// Config is the helper's persistent configuration.
type Config struct {
	AutoConnect bool     `json:"auto_connect"`
	MTU         int      `json:"mtu"`
	RouteAll    bool     `json:"route_all"`
	ManageDNS   bool     `json:"manage_dns"`
	DNSServers  []string `json:"dns_servers"`
}

// DefaultConfig is used when no config file exists.
func DefaultConfig() Config {
	return Config{AutoConnect: false, MTU: 1380, RouteAll: true, ManageDNS: true}
}

// LogLine is one helper log entry.
type LogLine struct {
	N    int64  `json:"n"`
	Time string `json:"t"`
	Text string `json:"msg"`
}

// Request is a client command.
type Request struct {
	Cmd    string  `json:"cmd"`              // status | connect | disconnect | get_config | set_config | logs | version
	Config *Config `json:"config,omitempty"` // set_config
	Since  int64   `json:"since,omitempty"`  // logs: lines after this number
}

// Response answers a Request.
type Response struct {
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	Status *Status   `json:"status,omitempty"`
	Config *Config   `json:"config,omitempty"`
	Logs   []LogLine `json:"logs,omitempty"`
}

// Call sends one request to the helper and waits for the answer.
func Call(req Request, timeout time.Duration) (*Response, error) {
	conn, err := net.DialTimeout("unix", SocketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("the Tetherly helper is not running (%v)", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	data, _ := json.Marshal(req)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
