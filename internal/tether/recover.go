package tether

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/gousb"
	"github.com/rs/zerolog/log"
)

// How a bulk endpoint is brought back after errors. A transaction error on the
// Mac halts the xHCI pipe and every later transfer fails with LIBUSB_ERROR_PIPE
// until the halt is cleared (seen on the S22's bulk IN 0x81, 2026-10-23).
const (
	maxClears    = 5                      // halts cleared without one good transfer in between, then give up
	clearEvery   = 5                      // other errors in a row before a halt is cleared anyway
	retryPause   = 50 * time.Millisecond  // between retries of other errors
	clearBackoff = 200 * time.Millisecond // before the first clear; doubles for each further one
)

// endpointGuard follows the errors of one bulk endpoint: it retries, clears a
// halted endpoint and decides when the session cannot go on. One goroutine
// owns it.
type endpointGuard struct {
	name  string // "IN 0x81"
	addr  uint8
	clear func(addr uint8) error
	pause func(time.Duration)

	errs    int       // failed transfers since the last good one
	clears  int       // halts cleared since the last good transfer
	first   error     // the error that started this episode
	since   time.Time // when it started
	lastClr error     // the last clear's own error
}

func newGuard(dir string, addr uint8, clear func(uint8) error, pause func(time.Duration)) *endpointGuard {
	return &endpointGuard{name: fmt.Sprintf("%s 0x%02x", dir, addr), addr: addr, clear: clear, pause: pause}
}

// ok records a good transfer and logs the recovery, as one line, when there
// had been errors.
func (g *endpointGuard) ok() {
	if g.errs == 0 {
		return
	}
	log.Warn().Str("component", "relay").Str("endpoint", g.name).AnErr("cause", g.first).
		Int("errors", g.errs).Int("halts_cleared", g.clears).Dur("after", time.Since(g.since).Round(time.Millisecond)).
		Msg("USB endpoint recovered")
	g.errs, g.clears, g.first, g.lastClr = 0, 0, nil, nil
}

// fail handles a failed transfer: nil means try again, an error ends the session.
func (g *endpointGuard) fail(err error) error {
	if isNoDevice(err) {
		return fmt.Errorf("phone went away: %w", err)
	}
	if g.errs == 0 {
		g.first, g.since = err, time.Now()
	}
	g.errs++
	if !isStall(err) && g.errs%clearEvery != 0 {
		g.pause(retryPause)
		return nil
	}
	if g.clears >= maxClears {
		if g.lastClr != nil {
			return fmt.Errorf("USB %s keeps failing after %d halt clears (last clear: %v): %w", g.name, g.clears, g.lastClr, err)
		}
		return fmt.Errorf("USB %s keeps stalling after %d halt clears: %w", g.name, g.clears, err)
	}
	g.pause(clearBackoff << g.clears)
	g.clears++
	cerr := g.clear(g.addr)
	if errors.Is(cerr, context.Canceled) {
		return cerr
	}
	if isNoDevice(cerr) {
		return fmt.Errorf("phone went away: %w", cerr)
	}
	g.lastClr = cerr
	log.Debug().Str("component", "relay").Str("endpoint", g.name).AnErr("error", err).AnErr("clear_error", cerr).Int("clear", g.clears).Msg("Clearing endpoint halt")
	return nil
}

func isNoDevice(err error) bool {
	return errors.Is(err, gousb.ErrorNoDevice) || errors.Is(err, gousb.TransferNoDevice)
}

func isStall(err error) bool {
	return errors.Is(err, gousb.ErrorPipe) || errors.Is(err, gousb.TransferStall)
}
