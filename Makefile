FLAGS = CGO_ENABLED=0
GOBUILD = go build -trimpath -ldflags="-s -w"

build: web
	$(FLAGS) $(GOBUILD) -o stremio-lan .
build-arm64: web
	$(FLAGS) GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/stremio-lan-arm64 .
build-armv7: web
	$(FLAGS) GOOS=linux GOARCH=arm GOARM=7 $(GOBUILD) -o dist/stremio-lan-armv7 .
# Desktop app (Wails v3): needs CGO and, on Linux, libgtk-4-dev + libwebkitgtk-6.0-dev.
# Build it on the OS you target.
desktop: web
	CGO_ENABLED=1 go build -tags desktop -trimpath -ldflags="-s -w" -o stremio-lan-desktop .
web:
	cd web && npm ci && npm run build
test:
	go test ./...
.PHONY: build build-arm64 build-armv7 desktop web test
