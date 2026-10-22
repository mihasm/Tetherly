#!/bin/zsh
# Builds Tetherly.app (SwiftUI menu bar app + Go root helper + bundled libusb),
# signs it ad hoc and zips it into dist/.
#   scripts/build-app.sh            -> build/Tetherly.app, dist/Tetherly-<version>.zip
# Needs: Xcode command line tools, Go, Homebrew libusb (brew install libusb pkg-config).
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="$(cat VERSION)"
BUILD_NO="$(git rev-list --count HEAD 2>/dev/null || echo 1)"
APP=build/Tetherly.app
C=$APP/Contents
LIBUSB="$(pkg-config --variable=libdir libusb-1.0)/libusb-1.0.0.dylib"

rm -rf "$APP"
mkdir -p "$C/MacOS" "$C/Frameworks" "$C/Resources" "$C/Library/LaunchDaemons" dist

echo "-> helper (Go)"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$C/MacOS/tetherly-helper" ./cmd/tetherly-helper
cp "$LIBUSB" "$C/Frameworks/libusb-1.0.0.dylib"
chmod u+w "$C/Frameworks/libusb-1.0.0.dylib"
install_name_tool -id @rpath/libusb-1.0.0.dylib "$C/Frameworks/libusb-1.0.0.dylib"
OLD="$(otool -L "$C/MacOS/tetherly-helper" | awk '/libusb/ {print $1}')"
install_name_tool -change "$OLD" @executable_path/../Frameworks/libusb-1.0.0.dylib "$C/MacOS/tetherly-helper"

echo "-> app (Swift)"
swiftc -O -parse-as-library -target arm64-apple-macos13.0 \
  -o "$C/MacOS/Tetherly" app/Sources/*.swift

echo "-> resources"
sed -e "s/__VERSION__/$VERSION/" -e "s/__BUILD__/$BUILD_NO/" app/Resources/Info.plist > "$C/Info.plist"
cp app/Resources/dev.miha.tetherly.helper.plist "$C/Library/LaunchDaemons/"
ICON=build/icon
rm -rf "$ICON.iconset" && mkdir -p "$ICON.iconset"
swift scripts/make-icon.swift "$ICON.png"
for s in 16 32 128 256 512; do
  sips -z $s $s "$ICON.png" --out "$ICON.iconset/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) "$ICON.png" --out "$ICON.iconset/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICON.iconset" -o "$C/Resources/AppIcon.icns"
cp LICENSE "$C/Resources/LICENSE.txt"

echo "-> sign (ad hoc)"
codesign --force --sign - --timestamp=none "$C/Frameworks/libusb-1.0.0.dylib"
codesign --force --sign - --timestamp=none --identifier dev.miha.tetherly.helper "$C/MacOS/tetherly-helper"
codesign --force --sign - --timestamp=none "$APP"
codesign --verify --deep --strict --verbose=1 "$APP"

ZIP="dist/Tetherly-$VERSION.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"
echo "built $APP and $ZIP"
