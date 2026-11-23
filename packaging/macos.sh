#!/bin/sh
# Builds dist/WorldHop-<version>.dmg containing an (unsigned) WorldHop.app.
set -eu
VERSION="${VERSION:-dev}"; BIN="${BIN:-worldhop}"
SHORT=$(echo "$VERSION" | sed 's/^v//'); case "$SHORT" in [0-9]*) ;; *) SHORT=0.0.0 ;; esac
app=dist/dmg/WorldHop.app
rm -rf dist/dmg && mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$BIN" "$app/Contents/MacOS/worldhop"
chmod 755 "$app/Contents/MacOS/worldhop"
cat > "$app/Contents/Info.plist" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>WorldHop</string>
<key>CFBundleIdentifier</key><string>app.worldhop.WorldHop</string>
<key>CFBundleExecutable</key><string>worldhop</string>
<key>CFBundleVersion</key><string>$SHORT</string>
<key>CFBundleShortVersionString</key><string>$SHORT</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSUIElement</key><true/>
</dict></plist>
PL
ln -s /Applications dist/dmg/Applications
hdiutil create -volname WorldHop -srcfolder dist/dmg -ov -format UDZO "dist/WorldHop-$VERSION.dmg" >/dev/null
rm -rf dist/dmg
echo "wrote dist/WorldHop-$VERSION.dmg"
