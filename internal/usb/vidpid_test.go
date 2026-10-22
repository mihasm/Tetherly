package usb

import "testing"

func TestRNDISMatchingNeverTakesADB(t *testing.T) {
	cases := []struct {
		name             string
		class, sub, prot uint8
		rndis, adb       bool
	}{
		{"rndis wceis", 0xE0, 0x01, 0x03, true, false},
		{"rndis misc", 0xEF, 0x04, 0x01, true, false},
		{"rndis cdc vendor", 0x02, 0x02, 0xFF, true, false},
		{"adb", 0xFF, 0x42, 0x01, false, true},
		{"fastboot", 0xFF, 0x42, 0x03, false, true},
		{"samsung vendor mtp", 0xFF, 0xFF, 0x00, false, false},
		{"mtp", 0x06, 0x01, 0x01, false, false},
		{"cdc data", 0x0A, 0x00, 0x00, false, false},
		{"cdc acm modem", 0x02, 0x02, 0x01, false, false},
	}
	for _, c := range cases {
		if got := IsRNDISControl(c.class, c.sub, c.prot); got != c.rndis {
			t.Errorf("%s: IsRNDISControl = %v, want %v", c.name, got, c.rndis)
		}
		if got := IsADB(c.class, c.sub, c.prot); got != c.adb {
			t.Errorf("%s: IsADB = %v, want %v", c.name, got, c.adb)
		}
	}
}

func TestPhoneDetection(t *testing.T) {
	adbOnly := DeviceInfo{VendorID: 0x04e8, Interfaces: []InterfaceInfo{{0, 0xFF, 0xFF, 0}, {1, 0xFF, 0x42, 0x01}}, HasADB: true}
	if !adbOnly.IsAndroid() || adbOnly.HasRNDIS {
		t.Fatal("a Samsung in MTP+ADB mode must be a phone without tethering")
	}
	keyboard := DeviceInfo{VendorID: 0x05ac, Interfaces: []InterfaceInfo{{0, 0x03, 0x01, 0x01}}}
	if keyboard.IsAndroid() {
		t.Fatal("a keyboard is not a phone")
	}
}
