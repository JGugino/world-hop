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
release:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop executable..."
	@test -n "$(GOOGLE_CLIENT_ID)" || { echo "set GOOGLE_CLIENT_ID"; exit 1; }
	@go build -trimpath -ldflags "-s -w \
	  -X main.version=$(VERSION) \
	  -X $(LINK_PKG).DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
	  -X $(LINK_PKG).DefaultGoogleClientSecret=$(GOOGLE_CLIENT_SECRET) \
	  -X $(LICENSE_PKG).GumroadProductID=$(GUMROAD_PRODUCT_ID) \
	  -X $(LICENSE_PKG).PurchaseURL=$(PURCHASE_URL)" \
	  -o $(OUT) ./cmd/worldhop

# Installable packages from a release build. Run on the matching OS.
# deb/tar.gz: Linux. dmg: macOS. zip: Windows (see .github/workflows/release.yml).
package-linux: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/linux.sh
package-macos: release
	@VERSION=$(VERSION) BIN=$(OUT) sh packaging/macos.sh

.PHONY: default release package-linux package-macos
