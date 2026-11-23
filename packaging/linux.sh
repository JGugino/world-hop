#!/bin/sh
# Builds dist/worldhop_<version>_amd64.deb and a .tar.gz from the binary $BIN.
set -eu
VERSION="${VERSION:-dev}"; BIN="${BIN:-worldhop}"
DEBVER=$(echo "$VERSION" | sed 's/^v//; s/[^0-9A-Za-z.+~-]/./g')
case "$DEBVER" in [0-9]*) ;; *) DEBVER="0~$DEBVER" ;; esac
ARCH=$(dpkg --print-architecture 2>/dev/null || echo amd64)
here=$(cd "$(dirname "$0")" && pwd)
root=$(mktemp -d); trap 'rm -rf "$root"' EXIT
mkdir -p dist "$root/usr/bin" "$root/usr/share/applications" "$root/usr/share/icons/hicolor/256x256/apps" "$root/DEBIAN"
install -m 0755 "$BIN" "$root/usr/bin/worldhop"
install -m 0644 "$here/worldhop.desktop" "$root/usr/share/applications/worldhop.desktop"
install -m 0644 "$here/../internal/tray/icon.png" "$root/usr/share/icons/hicolor/256x256/apps/worldhop.png"
cat > "$root/DEBIAN/control" <<CTL
Package: worldhop
Version: $DEBVER
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: WorldHop <noreply@example.invalid>
Description: Keep Minecraft worlds in sync between computers
 Lives in the system tray and syncs worlds through your own Google Drive.
 Not affiliated with Mojang or Microsoft.
CTL
dpkg-deb --root-owner-group --build "$root" "dist/worldhop_${DEBVER}_${ARCH}.deb" >/dev/null
tar -czf "dist/worldhop-${VERSION}-linux-${ARCH}.tar.gz" -C "$(dirname "$BIN")" "$(basename "$BIN")" 
echo "wrote dist/"
