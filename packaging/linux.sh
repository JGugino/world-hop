#!/bin/sh
# Builds dist/worldhop_<version>_amd64.deb and a .tar.gz from the binary $BIN.
set -eu
VERSION="${VERSION:-dev}"; BIN="${BIN:-worldhop}"
DEBVER=$(echo "$VERSION" | sed 's/^v//; s/[^0-9A-Za-z.+~-]/./g')
case "$DEBVER" in [0-9]*) ;; *) DEBVER="0~$DEBVER" ;; esac
ARCH=$(dpkg --print-architecture 2>/dev/null || echo amd64)
here=$(cd "$(dirname "$0")" && pwd)
root=$(mktemp -d); trap 'rm -rf "$root"' EXIT
chmod 0755 "$root" # mktemp makes it 0700, which dpkg would apply to "/"
mkdir -p dist "$root/usr/bin" "$root/usr/share/applications" "$root/usr/share/icons/hicolor/256x256/apps" "$root/DEBIAN"
install -m 0755 "$BIN" "$root/usr/bin/worldhop"
install -m 0644 "$here/worldhop.desktop" "$root/usr/share/applications/worldhop.desktop"
install -m 0644 "$here/../assets/icon/png/icon-256.png" "$root/usr/share/icons/hicolor/256x256/apps/worldhop.png"
cat > "$root/DEBIAN/control" <<CTL
Package: worldhop
Version: $DEBVER
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: Gugino Software Solutions <gugino@gugino.org>
Homepage: https://github.com/JGugino/world-hop
Recommends: xdg-utils
Suggests: gnome-shell-extension-appindicator
Description: Keep Minecraft worlds in sync between computers
 Lives in the system tray and syncs worlds through your own Google Drive
 or, with WorldHop Pro, your own S3-compatible storage.
 The tray icon needs a desktop with AppIndicator support (on GNOME,
 install gnome-shell-extension-appindicator).
 Not affiliated with Mojang or Microsoft.
CTL
dpkg-deb --root-owner-group --build "$root" "dist/worldhop_${DEBVER}_${ARCH}.deb" >/dev/null
# The tarball holds the binary as "worldhop" (the build's file name is longer), as the user guide says.
tar --owner=0 --group=0 --numeric-owner -czf "dist/worldhop-${VERSION}-linux-${ARCH}.tar.gz" -C "$root/usr/bin" worldhop
echo "wrote dist/"
