.PHONY: build test test-chromium cross

build:
	go build -o bin/kitty-browse ./cmd/kitty-browse

test:
	go test ./...

test-chromium:
	@test -n "$(KITTY_BROWSE_CHROME)" || (printf 'Set KITTY_BROWSE_CHROME to the Chromium executable path\n'; exit 1)
	go test ./internal/browser ./internal/render -run 'Integration|TestChromiumFrames' -count=1 -v

# The embedded library must build for golder's platforms even where the
# owner-only control socket is unavailable.
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
