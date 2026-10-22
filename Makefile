.PHONY: build test test-chromium

build:
	go build -o bin/kitty-browse ./cmd/kitty-browse

test:
	go test ./...

test-chromium:
	@test -n "$(KITTY_BROWSE_CHROME)" || (printf 'Set KITTY_BROWSE_CHROME to the Chromium executable path\n'; exit 1)
	go test ./internal/browser ./internal/render -run 'Integration|TestChromiumFrames' -count=1 -v
