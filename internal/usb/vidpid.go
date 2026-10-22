package usb

// USB interface signatures.
//
// Tetherly only ever opens a device that currently exposes an RNDIS control
// interface (the phone has "USB tethering" switched on), and only ever claims
// that interface and its CDC data interface. Every other interface of the
// phone — ADB, MTP, fastboot, audio — is left alone, so `adb` keeps working
// while tethering runs.
//
// The original DroidTether matched any vendor-specific (0xFF) interface of a
// known Android vendor as "RNDIS". The ADB interface is exactly that
// (FF/42/01), so the daemon opened and claimed the phone's ADB interface even
// with tethering off. That is gone: matching is by class triple only.

const (
	// RNDIS as Android exposes it with the "wceis" option (most phones,
	// Samsung included): Wireless Controller / RF / RNDIS.
	RNDISClass    uint8 = 0xE0
	RNDISSubClass uint8 = 0x01
	RNDISProtocol uint8 = 0x03

	// RNDIS over the Misc class (some MediaTek / Qualcomm builds).
	MiscClass         uint8 = 0xEF
	MiscRNDISSubClass uint8 = 0x04
	MiscRNDISProtocol uint8 = 0x01

	// Plain Linux f_rndis without wceis: CDC Communications / ACM / vendor.
	CDCClass       uint8 = 0x02
	CDCACMSubClass uint8 = 0x02
	CDCVendorProto uint8 = 0xFF

	// CDC Data, the interface that carries the bulk endpoints.
	CDCDataClass uint8 = 0x0A

	// ADB: vendor class, subclass 0x42, protocol 0x01. Fastboot is protocol 0x03.
	VendorClass uint8 = 0xFF
	ADBSubClass uint8 = 0x42
	ADBProtocol uint8 = 0x01

	// MTP / PTP (still image class).
	ImageClass uint8 = 0x06
)

// IsRNDISControl reports whether an interface triple is an RNDIS control interface.
func IsRNDISControl(class, subClass, proto uint8) bool {
	switch {
	case class == RNDISClass && subClass == RNDISSubClass && proto == RNDISProtocol:
		return true
	case class == MiscClass && subClass == MiscRNDISSubClass && proto == MiscRNDISProtocol:
		return true
	case class == CDCClass && subClass == CDCACMSubClass && proto == CDCVendorProto:
		return true
	}
	return false
}

// IsADB reports whether an interface triple is the Android Debug Bridge (or fastboot).
func IsADB(class, subClass, proto uint8) bool {
	return class == VendorClass && subClass == ADBSubClass
}

// Known Android vendor IDs, used only to label a plugged-in phone whose
// tethering is off ("phone found"). Never used to decide what to claim.
var knownAndroidVIDs = map[uint16]string{
	0x04e8: "Samsung",
	0x18d1: "Google",
	0x2717: "Xiaomi",
	0x22d9: "OPPO",
	0x2a70: "OnePlus",
	0x0fce: "Sony",
	0x05c6: "Qualcomm",
	0x12d1: "Huawei",
	0x2b0e: "Nothing",
	0x17ef: "Lenovo",
	0x22b8: "Motorola",
	0x1004: "LG",
	0x2d95: "vivo",
	0x0bb4: "HTC",
	0x2e04: "HMD",
	0x1ebf: "Fairphone",
}

// AndroidVendor returns a vendor name for a known Android VID.
func AndroidVendor(vid uint16) (string, bool) {
	name, ok := knownAndroidVIDs[vid]
	return name, ok
}
