package usb

import (
	"errors"
	"fmt"

	"github.com/google/gousb"
)

// Device is an opened phone with its RNDIS control and data interfaces claimed.
//
// Rules that keep adb (and everything else on the phone) working:
//   - no kernel-driver detach: on macOS libusb implements detach as a capture
//     re-enumeration of the WHOLE device, which kicks adb off the phone;
//   - no SET_CONFIGURATION: the RNDIS interface must be in the active
//     configuration, otherwise we refuse instead of switching configs;
//   - only the RNDIS control interface and its CDC Data interface are claimed;
//   - Close releases both interfaces and the handle, and closes the libusb context.
type Device struct {
	ctx          *gousb.Context
	usbd         *gousb.Device
	usbc         *gousb.Config
	controlIntf  *gousb.Interface
	dataIntf     *gousb.Interface
	InterfaceNum int
	Info         DeviceInfo
}

// ErrNoRNDIS means the phone does not expose RNDIS (USB tethering is off).
var ErrNoRNDIS = errors.New("the phone does not offer USB tethering right now (switch on USB tethering on the phone)")

// Open opens the device described by info and claims only its RNDIS interfaces.
func Open(info DeviceInfo) (*Device, error) {
	ctx := gousb.NewContext()
	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		if uint16(desc.Vendor) != info.VendorID || uint16(desc.Product) != info.ProductID {
			return false
		}
		if info.Address >= 0 && desc.Address != info.Address {
			return false
		}
		if info.Location != 0 && desc.Bus != info.Bus() {
			return false
		}
		return true
	})
	if err != nil && len(devs) == 0 {
		ctx.Close()
		return nil, fmt.Errorf("usb: cannot open %s: %w", info.VIDPID, err)
	}
	if len(devs) == 0 {
		ctx.Close()
		return nil, fmt.Errorf("usb: %s is no longer connected", info.VIDPID)
	}
	for _, extra := range devs[1:] {
		extra.Close()
	}
	dev := devs[0]
	d, err := claim(dev)
	if err != nil {
		dev.Close()
		ctx.Close()
		return nil, err
	}
	d.ctx = ctx
	d.Info = info
	return d, nil
}

func claim(dev *gousb.Device) (*Device, error) {
	active, err := dev.ActiveConfigNum()
	if err != nil {
		return nil, fmt.Errorf("usb: cannot read the active configuration: %w", err)
	}
	var cfgDesc *gousb.ConfigDesc
	for i := range dev.Desc.Configs {
		c := dev.Desc.Configs[i]
		if c.Number == active {
			cfgDesc = &c
			break
		}
	}
	if cfgDesc == nil {
		return nil, fmt.Errorf("usb: active configuration %d has no descriptor", active)
	}

	ctrlNum, ctrlAlt := -1, 0
	for _, intf := range cfgDesc.Interfaces {
		for _, alt := range intf.AltSettings {
			if IsRNDISControl(uint8(alt.Class), uint8(alt.SubClass), uint8(alt.Protocol)) {
				ctrlNum, ctrlAlt = intf.Number, alt.Alternate
				break
			}
		}
		if ctrlNum >= 0 {
			break
		}
	}
	if ctrlNum < 0 {
		return nil, ErrNoRNDIS
	}

	// The data interface is a CDC Data interface with a bulk IN/OUT pair,
	// normally the one right after the control interface.
	dataNum, dataAlt := -1, 0
	pick := func(intf gousb.InterfaceDesc) bool {
		for _, alt := range intf.AltSettings {
			if uint8(alt.Class) == CDCDataClass && hasBulkPair(alt) {
				dataNum, dataAlt = intf.Number, alt.Alternate
				return true
			}
		}
		return false
	}
	for _, intf := range cfgDesc.Interfaces {
		if intf.Number == ctrlNum+1 && pick(intf) {
			break
		}
	}
	if dataNum < 0 {
		for _, intf := range cfgDesc.Interfaces {
			if intf.Number != ctrlNum && pick(intf) {
				break
			}
		}
	}
	if dataNum < 0 {
		return nil, fmt.Errorf("usb: RNDIS data interface not found")
	}

	// Config() only issues SET_CONFIGURATION when the number differs from
	// the active one, which it never does here; autodetach stays off.
	usbc, err := dev.Config(active)
	if err != nil {
		return nil, fmt.Errorf("usb: cannot use configuration %d: %w", active, err)
	}
	ctrl, err := usbc.Interface(ctrlNum, ctrlAlt)
	if err != nil {
		usbc.Close()
		return nil, fmt.Errorf("usb: cannot claim RNDIS control interface %d: %w", ctrlNum, err)
	}
	data, err := usbc.Interface(dataNum, dataAlt)
	if err != nil {
		ctrl.Close()
		usbc.Close()
		return nil, fmt.Errorf("usb: cannot claim RNDIS data interface %d: %w", dataNum, err)
	}
	return &Device{usbd: dev, usbc: usbc, controlIntf: ctrl, dataIntf: data, InterfaceNum: ctrlNum}, nil
}

func hasBulkPair(alt gousb.InterfaceSetting) bool {
	var in, out bool
	for _, ep := range alt.Endpoints {
		if ep.TransferType == gousb.TransferTypeBulk {
			if ep.Direction == gousb.EndpointDirectionIn {
				in = true
			} else {
				out = true
			}
		}
	}
	return in && out
}

// Close releases the claimed interfaces, the configuration, the device handle
// and the libusb context. Safe to call more than once.
func (d *Device) Close() error {
	if d.dataIntf != nil {
		d.dataIntf.Close()
		d.dataIntf = nil
	}
	if d.controlIntf != nil {
		d.controlIntf.Close()
		d.controlIntf = nil
	}
	if d.usbc != nil {
		d.usbc.Close()
		d.usbc = nil
	}
	if d.usbd != nil {
		d.usbd.Close()
		d.usbd = nil
	}
	if d.ctx != nil {
		d.ctx.Close()
		d.ctx = nil
	}
	return nil
}

// Control performs a control transfer on the device.
func (d *Device) Control(rType, request uint8, val, idx uint16, data []byte) (int, error) {
	if d.usbd == nil {
		return 0, gousb.ErrorNoDevice
	}
	return d.usbd.Control(rType, request, val, idx, data)
}

// OpenBulkEndpoints returns the bulk IN and OUT endpoints of the data interface.
func (d *Device) OpenBulkEndpoints() (in *gousb.InEndpoint, out *gousb.OutEndpoint, err error) {
	if d.dataIntf == nil {
		return nil, nil, fmt.Errorf("usb: data interface not claimed")
	}
	for _, ep := range d.dataIntf.Setting.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.Direction == gousb.EndpointDirectionIn && in == nil {
			in, err = d.dataIntf.InEndpoint(ep.Number)
		} else if ep.Direction == gousb.EndpointDirectionOut && out == nil {
			out, err = d.dataIntf.OutEndpoint(ep.Number)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if in == nil || out == nil {
		return nil, nil, fmt.Errorf("usb: bulk endpoint pair not found")
	}
	return in, out, nil
}
