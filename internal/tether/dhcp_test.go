package tether

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestDHCPAckParsing(t *testing.T) {
	ip := make([]byte, 28+240+32)
	ip[0] = 0x45
	ip[9] = 17
	copy(ip[12:16], net.IPv4(192, 168, 42, 129).To4())
	binary.BigEndian.PutUint16(ip[20:22], 67)
	binary.BigEndian.PutUint16(ip[22:24], 68)
	copy(ip[28+16:28+20], net.IPv4(192, 168, 42, 50).To4())
	copy(ip[28+236:], []byte{0x63, 0x82, 0x53, 0x63})
	opts := []byte{53, 1, 5, 54, 4, 192, 168, 42, 129, 51, 4, 0, 0, 0x0e, 0x10, 6, 8, 192, 168, 42, 129, 8, 8, 4, 4, 255}
	copy(ip[28+240:], opts)

	r := &Relay{leased: make(chan struct{})}
	var gotGW, gotIP string
	var gotDNS []string
	var gotLease time.Duration
	r.OnDHCP = func(gw, client string, dns []string, lease time.Duration) {
		gotGW, gotIP, gotDNS, gotLease = gw, client, dns, lease
	}
	r.tryExtractDHCP(ip)
	if gotGW != "192.168.42.129" || gotIP != "192.168.42.50" {
		t.Fatalf("gateway/ip = %s/%s", gotGW, gotIP)
	}
	if len(gotDNS) != 2 || gotDNS[0] != "192.168.42.129" || gotDNS[1] != "8.8.4.4" {
		t.Fatalf("dns = %v", gotDNS)
	}
	if gotLease != time.Hour {
		t.Fatalf("lease = %v", gotLease)
	}
	select {
	case <-r.leased:
	default:
		t.Fatal("leased channel not closed")
	}
	// a renewal ACK must not call OnDHCP again
	called := false
	r.OnDHCP = func(string, string, []string, time.Duration) { called = true }
	r.tryExtractDHCP(ip)
	if called {
		t.Fatal("OnDHCP called on renewal")
	}
}
