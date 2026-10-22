# Contributing

Bug reports with the output of `tetherly devices` (which lists your phone's USB interfaces) are the most useful thing: phones differ in how they expose RNDIS.

Build: `brew install libusb pkg-config`, then `make app` (or `make install` to copy it into /Applications). `go test ./...` covers interface matching and DHCP parsing; the rest needs a phone.

Ground rules for changes in the USB layer: never detach kernel drivers, never issue SET_CONFIGURATION, never claim an interface that is not RNDIS — that is what keeps `adb` working next to Tetherly.
