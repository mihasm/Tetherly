package usb

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/IOKitLib.h>
#include <CoreFoundation/CoreFoundation.h>
#include <string.h>

typedef struct { int cls, sub, proto, num; } tl_intf;
typedef struct {
	int vid, pid, address;
	unsigned int location;
	char product[128], vendor[128], serial[128];
	int nintf;
	tl_intf intf[32];
} tl_dev;

static long long tl_num(io_registry_entry_t e, const char *key, long long def) {
	CFStringRef k = CFStringCreateWithCString(NULL, key, kCFStringEncodingUTF8);
	CFTypeRef v = IORegistryEntryCreateCFProperty(e, k, kCFAllocatorDefault, 0);
	CFRelease(k);
	long long out = def;
	if (v) {
		if (CFGetTypeID(v) == CFNumberGetTypeID()) CFNumberGetValue((CFNumberRef)v, kCFNumberLongLongType, &out);
		CFRelease(v);
	}
	return out;
}

static void tl_str(io_registry_entry_t e, const char *key, char *buf, int len) {
	buf[0] = 0;
	CFStringRef k = CFStringCreateWithCString(NULL, key, kCFStringEncodingUTF8);
	CFTypeRef v = IORegistryEntryCreateCFProperty(e, k, kCFAllocatorDefault, 0);
	CFRelease(k);
	if (v) {
		if (CFGetTypeID(v) == CFStringGetTypeID()) CFStringGetCString((CFStringRef)v, buf, len, kCFStringEncodingUTF8);
		CFRelease(v);
	}
}

// Lists USB devices and the interfaces of their active configuration straight
// from the I/O Registry. Nothing is opened: this never disturbs adb or any
// other client of the device.
static int tl_list(tl_dev *out, int max) {
	io_iterator_t it = 0;
	if (IOServiceGetMatchingServices(kIOMainPortDefault, IOServiceMatching("IOUSBHostDevice"), &it) != KERN_SUCCESS) return -1;
	int n = 0;
	io_service_t dev;
	while ((dev = IOIteratorNext(it)) != 0) {
		if (n >= max) { IOObjectRelease(dev); continue; }
		tl_dev *d = &out[n];
		memset(d, 0, sizeof(*d));
		d->vid = (int)tl_num(dev, "idVendor", 0);
		d->pid = (int)tl_num(dev, "idProduct", 0);
		d->address = (int)tl_num(dev, "USB Address", -1);
		d->location = (unsigned int)tl_num(dev, "locationID", 0);
		tl_str(dev, "USB Product Name", d->product, sizeof(d->product));
		tl_str(dev, "USB Vendor Name", d->vendor, sizeof(d->vendor));
		tl_str(dev, "USB Serial Number", d->serial, sizeof(d->serial));
		io_iterator_t cit = 0;
		if (IORegistryEntryGetChildIterator(dev, kIOServicePlane, &cit) == KERN_SUCCESS) {
			io_registry_entry_t c;
			while ((c = IOIteratorNext(cit)) != 0) {
				if (IOObjectConformsTo(c, "IOUSBHostInterface") && d->nintf < 32) {
					tl_intf *f = &d->intf[d->nintf++];
					f->cls = (int)tl_num(c, "bInterfaceClass", -1);
					f->sub = (int)tl_num(c, "bInterfaceSubClass", -1);
					f->proto = (int)tl_num(c, "bInterfaceProtocol", -1);
					f->num = (int)tl_num(c, "bInterfaceNumber", -1);
				}
				IOObjectRelease(c);
			}
			IOObjectRelease(cit);
		}
		IOObjectRelease(dev);
		n++;
	}
	IOObjectRelease(it);
	return n;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// InterfaceInfo is one interface of a device's active configuration.
type InterfaceInfo struct {
	Number, Class, SubClass, Protocol int
}

// DeviceInfo describes a USB device as seen in the I/O Registry.
type DeviceInfo struct {
	VendorID   uint16          `json:"-"`
	ProductID  uint16          `json:"-"`
	Address    int             `json:"-"`
	Location   uint32          `json:"-"`
	Vendor     string          `json:"vendor"`
	Product    string          `json:"product"`
	Serial     string          `json:"serial"`
	VIDPID     string          `json:"vid_pid"`
	Interfaces []InterfaceInfo `json:"-"`
	HasRNDIS   bool            `json:"rndis"`
	HasADB     bool            `json:"adb"`
}

// Bus is the libusb bus number derived from the location ID (darwin backend).
func (d DeviceInfo) Bus() int { return int(d.Location >> 24) }

// IsAndroid reports whether this looks like an Android phone.
func (d DeviceInfo) IsAndroid() bool {
	if d.HasRNDIS || d.HasADB {
		return true
	}
	if _, ok := AndroidVendor(d.VendorID); ok {
		for _, i := range d.Interfaces {
			if uint8(i.Class) == ImageClass || uint8(i.Class) == VendorClass {
				return true
			}
		}
	}
	return false
}

// Discover lists Android phones currently plugged in, tethering-capable first.
func Discover() ([]DeviceInfo, error) {
	all, err := ListAll()
	if err != nil {
		return nil, err
	}
	var rndis, other []DeviceInfo
	for _, d := range all {
		if !d.IsAndroid() {
			continue
		}
		if d.HasRNDIS {
			rndis = append(rndis, d)
		} else {
			other = append(other, d)
		}
	}
	return append(rndis, other...), nil
}

// ListAll lists every USB device with the interfaces of its active configuration.
func ListAll() ([]DeviceInfo, error) {
	const max = 64
	buf := make([]C.tl_dev, max)
	n := int(C.tl_list((*C.tl_dev)(unsafe.Pointer(&buf[0])), C.int(max)))
	if n < 0 {
		return nil, fmt.Errorf("usb: cannot read the I/O Registry")
	}
	var all []DeviceInfo
	for i := 0; i < n; i++ {
		c := buf[i]
		d := DeviceInfo{
			VendorID:  uint16(c.vid),
			ProductID: uint16(c.pid),
			Address:   int(c.address),
			Location:  uint32(c.location),
			Product:   C.GoString(&c.product[0]),
			Vendor:    C.GoString(&c.vendor[0]),
			Serial:    C.GoString(&c.serial[0]),
		}
		d.VIDPID = fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)
		for j := 0; j < int(c.nintf); j++ {
			f := c.intf[j]
			ii := InterfaceInfo{Number: int(f.num), Class: int(f.cls), SubClass: int(f.sub), Protocol: int(f.proto)}
			d.Interfaces = append(d.Interfaces, ii)
			if IsRNDISControl(uint8(ii.Class), uint8(ii.SubClass), uint8(ii.Protocol)) {
				d.HasRNDIS = true
			}
			if IsADB(uint8(ii.Class), uint8(ii.SubClass), uint8(ii.Protocol)) {
				d.HasADB = true
			}
		}
		if name, ok := AndroidVendor(d.VendorID); ok && d.Vendor == "" {
			d.Vendor = name
		}
		all = append(all, d)
	}
	return all, nil
}
