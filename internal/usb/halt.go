package usb

/*
// libusb itself is linked through gousb; declaring the one call here keeps it
// from being linked twice.
struct libusb_device_handle;
int libusb_clear_halt(struct libusb_device_handle *dev_handle, unsigned char endpoint);
*/
import "C"

import (
	"reflect"
	"unsafe"

	"github.com/google/gousb"
)

// ClearHalt clears a halted (stalled) endpoint of the claimed data interface
// on both ends: libusb_clear_halt resets the host controller's pipe (on macOS
// ClearPipeStallBothEnds) and sends CLEAR_FEATURE(ENDPOINT_HALT) to the phone.
// A plain CLEAR_FEATURE control request would leave the Mac's side halted.
// It touches only that one endpoint: no reset, no SET_CONFIGURATION.
//
// The endpoint must have no transfer in flight.
func (d *Device) ClearHalt(addr uint8) error {
	h := libusbHandle(d.usbd)
	if h == nil {
		return gousb.ErrorNoDevice
	}
	if rc := C.libusb_clear_halt((*C.struct_libusb_device_handle)(h), C.uchar(addr)); rc != 0 {
		return gousb.Error(int32(rc))
	}
	return nil
}

// libusbHandle is the libusb_device_handle behind a gousb.Device. gousb keeps
// it unexported and offers no clear-halt; the field is checked by
// TestGousbHandleField so a gousb upgrade that renames it fails the tests
// rather than the tether.
func libusbHandle(dev *gousb.Device) unsafe.Pointer {
	if dev == nil {
		return nil
	}
	f := reflect.ValueOf(dev).Elem().FieldByName("handle")
	if !f.IsValid() || f.Kind() != reflect.Ptr {
		return nil
	}
	return unsafe.Pointer(f.Pointer())
}
