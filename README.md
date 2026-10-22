# Tetherly

Android USB tethering for Apple Silicon Macs, from the menu bar. No kernel extension, no SIP changes.

Plug in an Android phone, switch on **USB tethering** on the phone, click **Connect** in the menu bar. Tetherly talks RNDIS to the phone over libusb and routes the Mac's traffic through a `utun` interface.

Tetherly is a fork of [DroidTether](https://github.com/HelloPrincePal/DroidTether) by **HelloPrincePal (Prince Pal)**, which did the hard part: the userspace RNDIS implementation, the DHCP exchange with the phone and the Samsung MAC workaround. Tetherly keeps that core and adds a menu bar app, an on-demand helper and a USB layer that leaves the rest of the phone alone. MIT licence, as upstream.

## What it does differently from DroidTether

- **adb keeps working.** DroidTether's always-running daemon broke `adb` on the phone. Two causes, both fixed:
  1. Its matcher treated any vendor-specific (`0xFF`) interface of a known Android vendor as RNDIS. ADB is exactly such an interface (`FF/42/01`), so with tethering *off* the daemon opened the phone every 500 ms and tried to use the ADB interface as its RNDIS control interface.
  2. It enabled libusb's kernel-driver auto-detach and switched configurations. On macOS, libusb implements "detach" by re-enumerating the **whole** device in capture mode, which drops every other client of the phone, adb included.

  Tetherly finds phones by reading the I/O Registry (nothing is opened), matches RNDIS by its exact class triple only, never detaches drivers, never sends `SET_CONFIGURATION`, claims only the RNDIS control and CDC data interfaces, and releases them, the device handle and the libusb context on disconnect.
- **Nothing runs until you want it.** The root helper is started by launchd when the app asks for the status, and exits after 90 s idle when it is not connected and auto-connect is off. It touches the phone only when you click Connect, or when you switch on auto-connect *and* the phone itself offers USB tethering.
- **Your local networks stay local.** All traffic goes through the phone via two `/1` routes, which never override directly connected subnets. With Wi-Fi on a network without internet (an OBD adapter at `192.168.0.10`, a camera, a printer), that subnet stays on Wi-Fi and everything else uses the phone.
- **DNS and "offline" Safari fixed.** The tether link is published to macOS as a network service with `OverridePrimary`, the way VPN clients do it. macOS then uses its DNS for all lookups and Safari, the App Store and `networkQuality` see the Mac as online, without a dummy Wi-Fi network. Nothing is written to your network preferences; it disappears on disconnect, and a helper that crashed cleans up after itself on its next start.
- DHCP is retried until the phone answers and renewed at half the lease; the phone's own DNS servers are used; the session ends cleanly when the cable is pulled or tethering is switched off on the phone.

## Install

1. Download `Tetherly-<version>.zip` from [Releases](https://github.com/mihasm/Tetherly/releases), unzip it and move **Tetherly.app** to **Applications**.
2. The build is signed ad hoc, not notarised. The first time, right-click the app and choose **Open**, or run:
   ```bash
   xattr -dr com.apple.quarantine /Applications/Tetherly.app
   ```
3. Open Tetherly, click the menu bar icon, then **Install Helper**. macOS asks you to allow it in **System Settings > General > Login Items & Extensions** (an administrator password): switch on **Tetherly**.
4. On the phone: **Settings > Connections > Mobile Hotspot and Tethering > USB tethering** (Samsung), or **Network & internet > Hotspot & tethering > USB tethering**. Then **Connect**.

Requirements: Apple Silicon, macOS 13 or later.

## The menu bar app

- State at a glance: no phone, USB tethering off, ready, connecting, connected (interface, IP, gateway, DNS, MTU, uptime, live throughput, totals) or error with a hint.
- Connect / Disconnect, auto-connect, open at login, notifications (connected, dropped, phone ready).
- Settings: MTU with presets (lower it if pages hang while ping works — an MTU black hole on the mobile network), route everything or only bring up the interface, primary-network DNS on or off, custom DNS servers.
- Log window with filter, follow, copy and a link to `/Library/Logs/Tetherly/tetherly.log`.

## Command line

Settings > Command-line tool links `~/.local/bin/tetherly`; the same commands also work as `/Applications/Tetherly.app/Contents/MacOS/Tetherly <command>`.

```
tetherly status [--json]     state, interface, IP, DNS, traffic
tetherly connect             connect and wait for an address
tetherly disconnect          disconnect and release the phone
tetherly logs [-f]           helper log
tetherly config mtu=1380 auto_connect=true dns_servers=1.1.1.1,9.9.9.9
tetherly devices             USB devices and their interfaces (read-only; attach to bug reports)
```

## For other programs

The helper answers on `/var/run/tetherly.sock`, one JSON line each way:

```
$ echo '{"cmd":"status"}' | socat - UNIX-CONNECT:/var/run/tetherly.sock
{"ok":true,"status":{"state":"connected","phone":{"vendor":"Samsung","product":"SM-S901B",...},
 "interface":"utun6","ip":"192.168.42.50","gateway":"192.168.42.129","dns":["192.168.42.129","1.1.1.1"],
 "dns_managed":true,"routed":true,"mtu":1380,"since":"2026-10-22T11:02:03+02:00",
 "rx_bytes":1234567,"tx_bytes":234567,"auto_connect":false,"version":"0.9.0"}}
```

`state` is one of `no_phone`, `phone_found` (plugged in, USB tethering off), `tethering_available`, `connecting`, `connected`, `disconnecting`, `error`. Anyone can read `status`, `logs` and `get_config`; `connect`, `disconnect` and `set_config` need an administrator or the logged-in user. Commands are listed in `internal/ipc/ipc.go`.

## Uninstall

Settings > Helper > Uninstall, quit Tetherly, move it to the Bin. Remove `/Library/Application Support/Tetherly` and `/Library/Logs/Tetherly` if you want the settings and logs gone too.

## Build

```bash
brew install libusb pkg-config
make app        # build/Tetherly.app and dist/Tetherly-<version>.zip
make install    # copy into /Applications and open it
go test ./...
```

Layout: `internal/usb` (I/O Registry discovery, interface claiming), `internal/rndis` (RNDIS control messages), `internal/tether` (packet relay, DHCP, session lifecycle), `internal/netcfg` (routes and DNS), `internal/helper` (root daemon, socket server), `cmd/tetherly-helper` (daemon and CLI), `app/` (SwiftUI menu bar app). The helper is registered with `SMAppService` from `Contents/Library/LaunchDaemons/dev.miha.tetherly.helper.plist` and runs `Contents/MacOS/tetherly-helper`; nothing is installed outside the app bundle except its settings, log and socket.

## Limits

- IPv4 only (as upstream): IPv6 traffic is dropped on the tether link.
- One phone at a time.
- Tested phones upstream: Xiaomi 11 Lite NE, Samsung Galaxy S24, Galaxy A55.

## Credits and licence

MIT. Copyright (c) 2026 PrincePal (DroidTether) and Miha Smrekar (Tetherly). See [LICENSE](LICENSE).
