package tether

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/mihasm/tetherly/internal/rndis"
	"github.com/mihasm/tetherly/internal/tun"
	"github.com/mihasm/tetherly/internal/usb"
	"github.com/rs/zerolog/log"
)

// bulkIn and bulkOut are the parts of gousb's endpoints the relay uses.
type bulkIn interface {
	ReadContext(ctx context.Context, buf []byte) (int, error)
}

type bulkOut interface {
	WriteContext(ctx context.Context, buf []byte) (int, error)
}

// Relay handles the packet shuttle between USB Bulk and Tunnel interface.
type Relay struct {
	tun    tun.Interface
	ctx    context.Context
	cancel context.CancelFunc
	errc   chan error

	usbIn    bulkIn
	usbOut   bulkOut
	inGuard  *endpointGuard
	outGuard *endpointGuard // guarded by outMu, like every write to usbOut
	outMu    sync.Mutex
	clearFn  func(addr uint8) error
	clearMu  sync.Mutex // held while a halt is cleared; Stop waits for it
	stopped  bool

	phoneMAC []byte
	// OnDHCP is called once, on the first DHCPACK: the phone's gateway, our
	// address, the DNS servers the phone offers and the lease length.
	OnDHCP func(gateway, client string, dns []string, lease time.Duration)

	// Dynamic state set after DHCP
	mu       sync.Mutex
	clientIP net.IP
	serverIP net.IP
	leased   chan struct{}
	lease    time.Duration

	// Traffic stats
	sentBytes uint64
	recvBytes uint64
}

// NewRelay creates a new bidirectional relay.
func NewRelay(ctx context.Context, dev *usb.Device, tun tun.Interface, phoneMAC []byte) (*Relay, error) {
	in, out, err := dev.OpenBulkEndpoints()
	if err != nil {
		return nil, err
	}
	return newRelay(ctx, in, out, uint8(in.Desc.Address), uint8(out.Desc.Address), dev.ClearHalt, tun, phoneMAC), nil
}

func newRelay(ctx context.Context, in bulkIn, out bulkOut, inAddr, outAddr uint8, clearHalt func(uint8) error, tun tun.Interface, phoneMAC []byte) *Relay {
	ctx, cancel := context.WithCancel(ctx)
	r := &Relay{
		tun:      tun,
		ctx:      ctx,
		cancel:   cancel,
		errc:     make(chan error, 4),
		usbIn:    in,
		usbOut:   out,
		clearFn:  clearHalt,
		phoneMAC: phoneMAC,
		leased:   make(chan struct{}),
	}
	r.inGuard = newGuard("IN", inAddr, r.clearHalt, r.pause)
	r.outGuard = newGuard("OUT", outAddr, r.clearHalt, r.pause)
	return r
}

// die ends the relay with err (the first one wins).
func (r *Relay) die(err error) {
	select {
	case r.errc <- err:
	default:
	}
}

// send writes one RNDIS message to the phone. A failed write is retried, and
// a halted OUT endpoint cleared, until the guard gives up; then the relay ends.
func (r *Relay) send(pkt []byte) error {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	for {
		_, err := r.usbOut.WriteContext(r.ctx, pkt)
		if err == nil {
			r.outGuard.ok()
			return nil
		}
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		if ferr := r.outGuard.fail(err); ferr != nil {
			if r.ctx.Err() == nil {
				r.die(fmt.Errorf("relay: %w", ferr))
			}
			return ferr
		}
	}
}

// Stats returns the bytes sent to and received from the phone.
func (r *Relay) Stats() (sent, received uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sentBytes, r.recvBytes
}

// Start spawns the relay goroutines and blocks until context is cancelled or an error occurs.
func (r *Relay) Start() error {
	// USB reads are cancelled through r.ctx; the tun read ends when the owner
	// closes the interface. The device itself is closed by the owner too.

	// Mac -> Phone (Tunnel -> USB)
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-r.ctx.Done():
				return
			default:
				n, err := r.tun.Read(buf)
				if err != nil {
					if err != io.EOF && r.ctx.Err() == nil && !errors.Is(err, os.ErrClosed) {
						r.die(fmt.Errorf("relay: tun read error: %w", err))
					}
					return
				}

				// macOS utun header is 4 bytes [0 0 0 2] for IPv4.
				if n < 24 {
					continue
				}
				rawIP := buf[4:n]

				// Drop non-IPv4 packets (e.g. IPv6 from macOS background traffic)
				if rawIP[0]>>4 != 4 {
					if rawIP[0]>>4 == 6 {
						log.Trace().Str("component", "relay").Msg("Dropping IPv6 packet (unsupported)")
					}
					continue
				}

				// Construct Ethernet frame: [DstMAC(6)] [SrcMAC(6)] [EtherType(2)] [Payload]
				r.mu.Lock()
				eth := make([]byte, 14+len(rawIP))
				copy(eth[0:6], r.phoneMAC)
				r.mu.Unlock()
				copy(eth[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
				binary.BigEndian.PutUint16(eth[12:14], 0x0800)
				copy(eth[14:], rawIP)

				if log.Trace().Enabled() {
					r.inspectPacket(rawIP, "TO_PHONE")
				}

				// Wrap in RNDIS and send
				pkt := rndis.EncapsulatePacket(eth)
				if err := r.send(pkt); err != nil {
					return
				}

				r.mu.Lock()
				r.sentBytes += uint64(len(pkt))
				r.mu.Unlock()
			}
		}
	}()

	// Phone -> Mac (USB -> Tunnel)
	go func() {
		buf := make([]byte, 16384)
		for {
			select {
			case <-r.ctx.Done():
				return
			default:
				n, err := r.usbIn.ReadContext(r.ctx, buf)
				if err != nil {
					if r.ctx.Err() != nil {
						return
					}
					if ferr := r.inGuard.fail(err); ferr != nil {
						if r.ctx.Err() == nil {
							r.die(fmt.Errorf("relay: %w", ferr))
						}
						return
					}
					continue
				}
				r.inGuard.ok()

				if n == 0 {
					continue
				}

				offset := 0
				for offset < n {
					msg := buf[offset:n]
					if len(msg) < 8 {
						break
					}
					msgType := binary.LittleEndian.Uint32(msg[0:4])
					msgLen := int(binary.LittleEndian.Uint32(msg[4:8]))

					if msgLen == 0 || msgLen > len(msg) {
						break
					}

					if msgType == rndis.MsgPacket && msgLen > 44 {
						ethPkt, err := rndis.DecapsulatePacket(msg[:msgLen])
						if err == nil && len(ethPkt) > 14 {
							// Auto-detect phone's real MAC from received traffic.
							// Samsung devices randomize the active MAC on the tethering
							// interface, so the RNDIS-queried address may not match.
							r.autoDetectMAC(ethPkt[6:12])

							ethType := binary.BigEndian.Uint16(ethPkt[12:14])
							switch ethType {
							case 0x0800: // IPv4
								rawIP := ethPkt[14:]
								r.tryExtractDHCP(rawIP)

								// Forward to utun with AF_INET header
								outBuf := make([]byte, 4+len(rawIP))
								binary.BigEndian.PutUint32(outBuf[0:4], 2)
								copy(outBuf[4:], rawIP)
								if _, err := r.tun.Write(outBuf); err != nil {
									log.Error().Str("component", "relay").Err(err).Msg("Failed to write to utun interface")
								}
								r.mu.Lock()
								r.recvBytes += uint64(len(msg))
								r.mu.Unlock()
							case 0x0806: // ARP
								r.handleARP(ethPkt)
							}
						}
					}

					offset += msgLen
				}
			}
		}
	}()

	// DHCP + KeepAlive goroutine
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		// Build a dummy ARP for keepalive (prevents Android from dropping idle connections)
		dummyARP := r.buildARPRequest(net.IP{0, 0, 0, 0})
		dummyPkt := rndis.EncapsulatePacket(dummyARP)

		// Wait for RNDIS to settle, then DHCP: Discover every 3 s until the
		// first ACK, then renew at half the lease.
		time.Sleep(500 * time.Millisecond)
		discover := rndis.EncapsulatePacket(r.buildDHCPDiscover())
		log.Info().Str("component", "relay").Msg("Sending DHCP Discover...")
		_ = r.send(discover)
		retry := time.NewTicker(3 * time.Second)
		defer retry.Stop()
		var renew <-chan time.Time
		leased := r.leased

		for {
			select {
			case <-r.ctx.Done():
				return
			case <-retry.C:
				log.Info().Str("component", "relay").Msg("No DHCP lease yet, sending Discover again...")
				_ = r.send(discover)
			case <-leased:
				leased = nil
				retry.Stop()
				r.mu.Lock()
				half := r.lease / 2
				r.mu.Unlock()
				if half < 30*time.Second {
					half = 30 * time.Second
				}
				renew = time.After(half)
			case <-renew:
				r.mu.Lock()
				ip, srv, half := r.clientIP, r.serverIP, r.lease/2
				r.mu.Unlock()
				if half < 30*time.Second {
					half = 30 * time.Second
				}
				log.Info().Str("component", "relay").Str("ip", ip.String()).Msg("Renewing DHCP lease")
				_ = r.send(rndis.EncapsulatePacket(r.buildDHCPRequest(ip, srv)))
				renew = time.After(half)
			case <-ticker.C:
				_ = r.send(dummyPkt)
			}
		}
	}()

	log.Info().Str("component", "relay").Msg("Bidirectional packet relay started")

	select {
	case err := <-r.errc:
		r.cancel()
		return err
	case <-r.ctx.Done():
		return nil
	}
}

// autoDetectMAC updates phoneMAC if the received source MAC differs (Samsung MAC randomization workaround).
func (r *Relay) autoDetectMAC(srcMAC []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < 6; i++ {
		if r.phoneMAC[i] != srcMAC[i] {
			log.Warn().Str("component", "relay").
				Str("old_mac", hex.EncodeToString(r.phoneMAC)).
				Str("real_mac", hex.EncodeToString(srcMAC)).
				Msg("Phone MAC mismatch detected — updating to real MAC")
			copy(r.phoneMAC, srcMAC)
			return
		}
	}
}

// tryExtractDHCP checks if a raw IP packet is a DHCP message and handles Offer/ACK.
func (r *Relay) tryExtractDHCP(rawIP []byte) {
	// Minimum: IP(20) + UDP(8) + BOOTP(236) + MagicCookie(4) + Option(3)
	if len(rawIP) < 271 {
		return
	}
	if rawIP[9] != 17 { // UDP
		return
	}
	srcPort := binary.BigEndian.Uint16(rawIP[20:22])
	dstPort := binary.BigEndian.Uint16(rawIP[22:24])
	if srcPort != 67 || dstPort != 68 {
		return
	}

	bootpStart := 28 // IP(20) + UDP(8)

	// Verify DHCP magic cookie at BOOTP offset 236
	cookieOff := bootpStart + 236
	if len(rawIP) < cookieOff+4 {
		return
	}
	if rawIP[cookieOff] != 0x63 || rawIP[cookieOff+1] != 0x82 ||
		rawIP[cookieOff+2] != 0x53 || rawIP[cookieOff+3] != 0x63 {
		return
	}

	yiaddr := make(net.IP, 4)
	copy(yiaddr, rawIP[bootpStart+16:bootpStart+20])

	serverIP := make(net.IP, 4)
	copy(serverIP, rawIP[12:16])

	msgType := dhcpOption(rawIP, cookieOff+4, 53)
	var typ byte
	if len(msgType) >= 1 {
		typ = msgType[0]
	}

	switch typ {
	case 2: // DHCPOFFER
		log.Info().Str("component", "relay").
			Str("your_ip", yiaddr.String()).
			Str("server_ip", serverIP.String()).
			Msg("DHCPOFFER received → sending DHCP Request")
		reqPkt := r.buildDHCPRequest(yiaddr, serverIP)
		r.writeUSB(rndis.EncapsulatePacket(reqPkt))

	case 5: // DHCPACK
		log.Info().Str("component", "relay").
			Str("your_ip", yiaddr.String()).
			Str("server_ip", serverIP.String()).
			Msg("DHCPACK received — lease confirmed")

		var dns []string
		if opt := dhcpOption(rawIP, cookieOff+4, 6); len(opt) >= 4 {
			for i := 0; i+4 <= len(opt); i += 4 {
				dns = append(dns, net.IP(opt[i:i+4]).String())
			}
		}
		lease := time.Hour
		if opt := dhcpOption(rawIP, cookieOff+4, 51); len(opt) == 4 {
			lease = time.Duration(binary.BigEndian.Uint32(opt)) * time.Second
		}
		if opt := dhcpOption(rawIP, cookieOff+4, 54); len(opt) == 4 {
			serverIP = net.IP(append([]byte(nil), opt...))
		}

		r.mu.Lock()
		first := r.clientIP == nil
		r.clientIP = make(net.IP, 4)
		copy(r.clientIP, yiaddr)
		r.serverIP = serverIP
		r.lease = lease
		r.mu.Unlock()

		if first {
			close(r.leased)
			if r.OnDHCP != nil {
				r.OnDHCP(serverIP.String(), yiaddr.String(), dns, lease)
			}
		}

		r.sendGratuitousARP(yiaddr)

	case 6: // DHCPNAK
		log.Warn().Str("component", "relay").Msg("DHCPNAK received — server rejected our request")
	}
}

// dhcpOption returns the value of a DHCP option from the options block.
func dhcpOption(rawIP []byte, optStart int, want byte) []byte {
	pos := optStart
	for pos < len(rawIP)-1 {
		tag := rawIP[pos]
		if tag == 255 {
			break
		}
		if tag == 0 {
			pos++
			continue
		}
		if pos+1 >= len(rawIP) {
			break
		}
		length := int(rawIP[pos+1])
		if pos+2+length > len(rawIP) {
			break
		}
		if tag == want {
			return rawIP[pos+2 : pos+2+length]
		}
		pos += 2 + length
	}
	return nil
}

// handleARP responds to ARP requests for our dynamically-assigned IP.
func (r *Relay) handleARP(ethPkt []byte) {
	if len(ethPkt) < 42 {
		return
	}
	op := binary.BigEndian.Uint16(ethPkt[20:22])
	if op != 1 {
		return
	}

	targetIP := net.IP(ethPkt[38:42])
	senderIP := net.IP(ethPkt[28:32])

	r.mu.Lock()
	myIP := r.clientIP
	r.mu.Unlock()

	if myIP == nil || !myIP.Equal(targetIP) {
		return
	}

	log.Debug().Str("component", "relay").
		Str("who_has", targetIP.String()).
		Str("tell", senderIP.String()).
		Msg("ARP Request from phone — sending reply")

	reply := make([]byte, 42)
	copy(reply[0:6], ethPkt[6:12])
	copy(reply[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	binary.BigEndian.PutUint16(reply[12:14], 0x0806)

	binary.BigEndian.PutUint16(reply[14:16], 1)
	binary.BigEndian.PutUint16(reply[16:18], 0x0800)
	reply[18] = 6
	reply[19] = 4
	binary.BigEndian.PutUint16(reply[20:22], 2)

	copy(reply[22:28], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	copy(reply[28:32], myIP.To4())
	copy(reply[32:38], ethPkt[22:28])
	copy(reply[38:42], ethPkt[28:32])

	pkt := rndis.EncapsulatePacket(reply)
	r.writeUSB(pkt)
}

// sendGratuitousARP announces our MAC/IP binding to the phone.
func (r *Relay) sendGratuitousARP(ip net.IP) {
	garp := make([]byte, 42)
	copy(garp[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(garp[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	binary.BigEndian.PutUint16(garp[12:14], 0x0806)

	binary.BigEndian.PutUint16(garp[14:16], 1)
	binary.BigEndian.PutUint16(garp[16:18], 0x0800)
	garp[18] = 6
	garp[19] = 4
	binary.BigEndian.PutUint16(garp[20:22], 2)

	copy(garp[22:28], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	copy(garp[28:32], ip.To4())
	copy(garp[32:38], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(garp[38:42], ip.To4())

	log.Debug().Str("component", "relay").Str("ip", ip.String()).Msg("Sending gratuitous ARP")
	pkt := rndis.EncapsulatePacket(garp)
	r.writeUSB(pkt)
}

// buildARPRequest constructs a broadcast ARP request Ethernet frame.
func (r *Relay) buildARPRequest(targetIP net.IP) []byte {
	arp := make([]byte, 42)
	copy(arp[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(arp[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	binary.BigEndian.PutUint16(arp[12:14], 0x0806)
	binary.BigEndian.PutUint16(arp[14:16], 1)
	binary.BigEndian.PutUint16(arp[16:18], 0x0800)
	arp[18] = 6
	arp[19] = 4
	binary.BigEndian.PutUint16(arp[20:22], 1)
	copy(arp[22:28], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	if len(targetIP) >= 4 {
		copy(arp[38:42], targetIP.To4())
	}
	return arp
}

// buildDHCPDiscover constructs a DHCP Discover Ethernet frame.
func (r *Relay) buildDHCPDiscover() []byte {
	return r.buildDHCPPacket(1, nil, nil)
}

// buildDHCPRequest constructs a DHCP Request Ethernet frame.
func (r *Relay) buildDHCPRequest(requestedIP, serverIP net.IP) []byte {
	return r.buildDHCPPacket(3, requestedIP, serverIP)
}

// buildDHCPPacket constructs a DHCP Ethernet frame for Discover (type=1) or Request (type=3).
func (r *Relay) buildDHCPPacket(dhcpType byte, requestedIP, serverIP net.IP) []byte {
	dhcp := make([]byte, 342) // 14(Eth) + 20(IP) + 8(UDP) + 300(BOOTP/DHCP)

	// Ethernet Header (Broadcast)
	copy(dhcp[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(dhcp[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	binary.BigEndian.PutUint16(dhcp[12:14], 0x0800)

	// IPv4 Header
	dhcp[14] = 0x45
	binary.BigEndian.PutUint16(dhcp[16:18], 328)
	dhcp[22] = 64
	dhcp[23] = 17
	copy(dhcp[30:34], []byte{255, 255, 255, 255})

	// IPv4 checksum
	var sum uint32
	for i := 14; i < 34; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(dhcp[i : i+2]))
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	binary.BigEndian.PutUint16(dhcp[24:26], ^uint16(sum))

	// UDP Header
	binary.BigEndian.PutUint16(dhcp[34:36], 68)
	binary.BigEndian.PutUint16(dhcp[36:38], 67)
	binary.BigEndian.PutUint16(dhcp[38:40], 308)

	// BOOTP Payload
	dhcp[42] = 1
	dhcp[43] = 1
	dhcp[44] = 6
	binary.BigEndian.PutUint32(dhcp[46:50], 0x12345678)
	binary.BigEndian.PutUint16(dhcp[52:54], 0x8000)
	copy(dhcp[70:76], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})

	// DHCP Magic Cookie
	copy(dhcp[278:282], []byte{0x63, 0x82, 0x53, 0x63})

	// DHCP Options
	optOff := 282
	dhcp[optOff] = 53
	dhcp[optOff+1] = 1
	dhcp[optOff+2] = dhcpType
	optOff += 3

	if dhcpType == 3 && requestedIP != nil && serverIP != nil {
		dhcp[optOff] = 50
		dhcp[optOff+1] = 4
		copy(dhcp[optOff+2:optOff+6], requestedIP.To4())
		optOff += 6

		dhcp[optOff] = 54
		dhcp[optOff+1] = 4
		copy(dhcp[optOff+2:optOff+6], serverIP.To4())
		optOff += 6
	}

	dhcp[optOff] = 255
	return dhcp
}

// inspectPacket logs IP packet details for debugging (only called at Trace level).
func (r *Relay) inspectPacket(data []byte, direction string) {
	if len(data) < 20 {
		return
	}
	if data[0]>>4 != 4 {
		return
	}
	src := fmt.Sprintf("%d.%d.%d.%d", data[12], data[13], data[14], data[15])
	dst := fmt.Sprintf("%d.%d.%d.%d", data[16], data[17], data[18], data[19])
	log.Trace().Str("component", "relay").Str("dir", direction).Str("src", src).Str("dst", dst).Msg("packet")
}

// Stop shuts down the relay.
func (r *Relay) Stop() {
	r.cancel()
	// wait for a halt clear in progress: the owner closes the device next
	r.clearMu.Lock()
	r.stopped = true
	r.clearMu.Unlock()
}

// clearHalt clears an endpoint halt unless the relay has stopped.
func (r *Relay) clearHalt(addr uint8) error {
	r.clearMu.Lock()
	defer r.clearMu.Unlock()
	if r.stopped || r.ctx.Err() != nil {
		return context.Canceled
	}
	return r.clearFn(addr)
}

// pause waits d, or less when the relay stops.
func (r *Relay) pause(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.ctx.Done():
	}
}

// writeUSB sends a control-plane frame (ARP, DHCP) to the phone.
func (r *Relay) writeUSB(pkt []byte) {
	if r.usbOut == nil {
		return
	}
	_ = r.send(pkt)
}
