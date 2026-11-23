default:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop executable..."
	@go build -o worldhop ./cmd/worldhop
	@echo "Starting WorldHop..."
	@./worldhop -store ~/Documents/projects/claude-projects/testing-shared

# Release build with the app's own IDs compiled in (see docs/cloud-setup.md and
# docs/licensing.md). Supply them from the environment or CI secrets; never
# commit them. GOOGLE_CLIENT_SECRET, GUMROAD_PRODUCT_ID, PURCHASE_URL and
# VERSION are optional (without a product ID the build cannot activate Pro).
LINK_PKG = github.com/JGugino/world-hopper/internal/link
# Local secrets for `make release` (gitignored); exported to the recipe.
-include .env
export

LICENSE_PKG = github.com/JGugino/world-hopper/internal/license
VERSION ?= dev
OUT ?= worldhop
RELEASE_LDFLAGS = -s -w \
  -X main.version=$(VERSION) \
  -X $(LINK_PKG).DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
  -X $(LINK_PKG).DefaultGoogleClientSecret=$(GOOGLE_CLIENT_SECRET) \
  -X $(LICENSE_PKG).GumroadProductID=$(GUMROAD_PRODUCT_ID) \
  -X $(LICENSE_PKG).PurchaseURL=$(PURCHASE_URL)

release:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop executable..."
	@test -n "$(GOOGLE_CLIENT_ID)" || { echo "set GOOGLE_CLIENT_ID"; exit 1; }
	@go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(OUT) ./cmd/worldhop

# Windows release (worldhop.exe) with the same IDs from the same .env. It is
# cross-compiled from any OS with CGO off, so it has no native webview and
# opens the default browser. -H windowsgui hides the console window. For the
# native webview build run this target's go command on Windows with
# CGO_ENABLED=1, a C compiler and `-tags webview` (see docs/cloud-setup.md).
OUT_WINDOWS ?= $(OUT).exe
release-windows: syso
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop Windows executable..."
	@test -n "$(GOOGLE_CLIENT_ID)" || { echo "set GOOGLE_CLIENT_ID"; exit 1; }
	@GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(RELEASE_LDFLAGS) -H windowsgui" -o $(OUT_WINDOWS) ./cmd/worldhop

# Windows exe icon resource (shown by Explorer and the taskbar). The generated
# .syso is committed, so this only refreshes it; it is linked automatically
# for windows/amd64 builds and ignored on other targets.
SYSO = cmd/worldhop/rsrc_windows_amd64.syso
syso:
	@go run github.com/akavel/rsrc@v0.10.2 -ico assets/icon/worldhop.ico -arch amd64 -o $(SYSO) \
	  || echo "rsrc unavailable; using the committed $(SYSO)"

# Regenerate PNGs, .ico, favicon and preview from assets/icon/icon.svg
# (needs Node + Playwright/Chromium), then the Windows resource.
icons:
	@node assets/icon/generate.cjs
	@$(MAKE) syso

# Linux and Windows release builds (UI is built once per target).
release-all: release release-windows

# Installable packages from a release build. Run on the matching OS.
# deb/tar.gz: Linux. dmg: macOS. zip: Windows (see .github/workflows/release.yml).
package-linux: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/linux.sh
package-macos: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/macos.sh

.PHONY: syso icons default release release-windows release-all package-linux package-macos
