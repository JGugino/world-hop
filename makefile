default:
	@go build -o worldhop ./cmd/worldhop
	@./worldhop -store ~/Documents/projects/claude-projects/testing-shared

# Release build with the app's own OAuth client IDs compiled in (see
# docs/cloud-setup.md). Supply them from the environment or CI secrets; never
# commit them. DROPBOX_REDIRECT_PORT and GOOGLE_CLIENT_SECRET are optional.
LINK_PKG = github.com/JGugino/world-hopper/internal/link
release:
	@test -n "$(GOOGLE_CLIENT_ID)" -a -n "$(DROPBOX_APP_KEY)" || { echo "set GOOGLE_CLIENT_ID and DROPBOX_APP_KEY"; exit 1; }
	@go build -trimpath -ldflags "-s -w \
	  -X $(LINK_PKG).DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
	  -X $(LINK_PKG).DefaultGoogleClientSecret=$(GOOGLE_CLIENT_SECRET) \
	  -X $(LINK_PKG).DefaultDropboxAppKey=$(DROPBOX_APP_KEY) \
	  -X $(LINK_PKG).DefaultDropboxRedirectPort=$(DROPBOX_REDIRECT_PORT)" \
	  -o worldhop ./cmd/worldhop

.PHONY: default release
