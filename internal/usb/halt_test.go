package usb

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/gousb"
)

// ClearHalt reads gousb's unexported device handle; this fails when an
// upgrade of gousb moves it.
func TestGousbHandleField(t *testing.T) {
	f, ok := reflect.TypeOf(gousb.Device{}).FieldByName("handle")
	if !ok {
		t.Fatal("gousb.Device has no field handle: ClearHalt cannot reach libusb")
	}
	if f.Type.Kind() != reflect.Ptr {
		t.Fatalf("gousb.Device.handle is a %s, want a pointer", f.Type.Kind())
	}
	if libusbHandle(&gousb.Device{}) != nil {
		t.Fatal("a closed device must have no handle")
	}
}

func TestClearHaltWithoutDevice(t *testing.T) {
	d := &Device{}
	if err := d.ClearHalt(0x81); !errors.Is(err, gousb.ErrorNoDevice) {
		t.Fatalf("ClearHalt on a closed device = %v, want ErrorNoDevice", err)
	}
}
