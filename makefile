default:
	@make release-all

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
# All built binaries (and installers from packaging/) land in DIST.
DIST ?= dist
# Native build for the current OS (`make release`). Override OUT to rename it.
OUT ?= $(DIST)/worldhop
RELEASE_LDFLAGS = -s -w \
  -X main.version=$(VERSION) \
  -X $(LINK_PKG).DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
  -X $(LINK_PKG).DefaultGoogleClientSecret=$(GOOGLE_CLIENT_SECRET) \
  -X $(LICENSE_PKG).GumroadProductID=$(GUMROAD_PRODUCT_ID) \
  -X $(LICENSE_PKG).PurchaseURL=$(PURCHASE_URL)

# Development run with cloud sign-in: builds the UI and a binary with the IDs
# from .env compiled in (same as a release), then runs it with the normal
# cloud store. Plain `make` uses -store, a shared folder, which turns cloud
# accounts off.
dev: ui
	@go build -ldflags "$(RELEASE_LDFLAGS)" -o $(DIST)/worldhop ./cmd/worldhop
	@$(DIST)/worldhop

UNAME_S := $(shell uname -s)

# Build the web UI (shared by every release target; make runs it once).
ui:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build

check-release-env:
	@test -n "$(GOOGLE_CLIENT_ID)" || { echo "set GOOGLE_CLIENT_ID"; exit 1; }

# Release build for the machine you are on.
release: ui check-release-env
	@echo "Building WorldHop executable..."
	@mkdir -p $(dir $(OUT))
	@go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(OUT) ./cmd/worldhop

# Linux release (dist/worldhop-linux-amd64). Cross-compiles from any OS with
# CGO off: no native webview, opens the default browser (same as the Windows
# build). For the native webview run `make release` on Linux with
# CGO_ENABLED=1 and `-tags webview`.
OUT_LINUX ?= $(DIST)/worldhop-linux-amd64
release-linux: ui check-release-env
	@echo "Building WorldHop Linux executable..."
	@mkdir -p $(dir $(OUT_LINUX))
	@GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(OUT_LINUX) ./cmd/worldhop

# Windows release (dist/worldhop-windows-amd64.exe) with the same IDs from the
# same .env. It is cross-compiled from any OS with CGO off, so it has no native
# webview and opens the default browser. -H windowsgui hides the console
# window. For the native webview build run this target's go command on Windows
# with CGO_ENABLED=1, a C compiler and `-tags webview` (see docs/cloud-setup.md).
OUT_WINDOWS ?= $(DIST)/worldhop-windows-amd64.exe
release-windows: ui syso check-release-env
	@echo "Building WorldHop Windows executable..."
	@mkdir -p $(dir $(OUT_WINDOWS))
	@GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(RELEASE_LDFLAGS) -H windowsgui" -o $(OUT_WINDOWS) ./cmd/worldhop

# macOS release: dist/worldhop-macos-arm64 (Apple silicon) and
# dist/worldhop-macos-amd64 (Intel). The tray needs cgo and Apple's SDK, so
# this must run on a Mac (Xcode command line tools); it cannot be
# cross-compiled from Linux or Windows. Set MAC_ARCHS=arm64 to build one.
MAC_ARCHS ?= arm64 amd64
OUT_MAC ?= $(DIST)/worldhop-macos
release-mac: ui check-release-env
	@test "$(UNAME_S)" = Darwin || { echo "release-mac must run on macOS (the tray needs cgo and Apple's SDK)"; exit 1; }
	@mkdir -p $(dir $(OUT_MAC))
	@for arch in $(MAC_ARCHS); do \
	  echo "Building WorldHop macOS $$arch executable..."; \
	  GOOS=darwin GOARCH=$$arch CGO_ENABLED=1 go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(OUT_MAC)-$$arch ./cmd/worldhop || exit 1; \
	done

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

# Linux, Windows and macOS builds into dist/ (UI is built once). The macOS
# build only works on a Mac, so elsewhere it is skipped with a warning.
release-all: release-linux release-windows
	@if [ "$(UNAME_S)" = Darwin ]; then $(MAKE) release-mac; \
	else echo "WARNING: skipped macOS build (run 'make release-mac' on a Mac)"; fi

# Installable packages from a release build. Run on the matching OS.
# deb/tar.gz: Linux. dmg: macOS. zip: Windows (see .github/workflows/release.yml).
package-linux: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/linux.sh
package-macos: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/macos.sh

.PHONY: syso icons default dev ui check-release-env release release-linux release-windows release-mac release-all package-linux package-macos
