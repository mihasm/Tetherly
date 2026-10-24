package helper

import (
	"time"

	"github.com/mihasm/tetherly/internal/usb"
)

const (
	// comebackWindow is how long a phone that left the bus during a wanted
	// connection may take to come back and still be reconnected.
	comebackWindow = 3 * time.Minute
	// tetherOffGrace is how long the phone may be on the bus without USB
	// tethering: a re-enumeration passes through the plain 6860 profile for
	// a second or two (2026-10-23 22:36), switching tethering off stays there.
	tetherOffGrace = 20 * time.Second
	// stableSession is how long a session must run before its end reconnects
	// at once instead of after the backoff.
	stableSession = time.Minute
)

// wanted is a connection the user asked for (connect from the CLI, the menu
// bar app or auto-connect). The helper keeps it up until disconnect, or until
// the phone stops offering USB tethering for good.
type wanted struct {
	phone    usb.DeviceInfo // the phone it was made with
	ended    time.Time      // when the last session ended (zero while one runs)
	reason   string         // why it ended
	gone     time.Time      // since when the phone is off the bus (zero: on it)
	noTether time.Time      // since when it is on the bus without USB tethering
}

type wishStep int

const (
	wishWait wishStep = iota
	wishReconnect
	wishDrop
)

// step looks at the phones on the bus while no session runs: wait, reconnect
// to the returned phone, or drop the wish (with the reason).
func (w *wanted) step(now time.Time, phones []usb.DeviceInfo) (wishStep, *usb.DeviceInfo, string) {
	var p *usb.DeviceInfo
	for i := range phones {
		if samePhone(&w.phone, &phones[i]) {
			p = &phones[i]
			break
		}
	}
	switch {
	case p == nil:
		w.noTether = time.Time{}
		if w.gone.IsZero() {
			w.gone = now
		}
		if now.Sub(w.gone) > comebackWindow {
			return wishDrop, nil, "the phone did not come back within " + comebackWindow.String()
		}
		return wishWait, nil, ""
	case !p.HasRNDIS:
		w.gone = time.Time{}
		if w.noTether.IsZero() {
			w.noTether = now
		}
		if now.Sub(w.noTether) > tetherOffGrace {
			return wishDrop, nil, "USB tethering was switched off on the phone"
		}
		return wishWait, nil, ""
	}
	w.gone, w.noTether = time.Time{}, time.Time{}
	return wishReconnect, p, ""
}

// samePhone matches a phone across re-enumerations, which change its product
// ID (6864 with tethering, 6860 without) and its address, not its serial.
func samePhone(a, b *usb.DeviceInfo) bool {
	if a == nil || b == nil || a.VendorID != b.VendorID {
		return false
	}
	if a.Serial != "" || b.Serial != "" {
		return a.Serial == b.Serial
	}
	return a.Product == b.Product
}

// backoff is the wait before the next attempt after n failures in a row:
// none, then 5 s, 10 s, 20 s, 40 s, 1 min.
func backoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := time.Duration(5<<min(n-1, 4)) * time.Second
	if d > time.Minute {
		d = time.Minute
	}
	return d
}
