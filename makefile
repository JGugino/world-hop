default:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop executable..."
	@cd .. & go build -o worldhop ./cmd/worldhop
	@echo "Starting WorldHop..."
	@./worldhop -store ~/Documents/projects/claude-projects/testing-shared

# Release build with the app's own OAuth client IDs compiled in (see
# docs/cloud-setup.md). Supply them from the environment or CI secrets; never
# commit them. GOOGLE_CLIENT_SECRET is optional.
LINK_PKG = github.com/JGugino/world-hopper/internal/link
-include .env
export
release:
	@echo "Building Web View UI..."
	@cd ui/ && pnpm build
	@echo "Building WorldHop executable..."
	@test -n "$(GOOGLE_CLIENT_ID)" || { echo "set GOOGLE_CLIENT_ID"; exit 1; }
	@go build -trimpath -ldflags "-s -w \
	  -X $(LINK_PKG).DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
	  -X $(LINK_PKG).DefaultGoogleClientSecret=$(GOOGLE_CLIENT_SECRET)" \
	  -o worldhop ./cmd/worldhop

.PHONY: default release
