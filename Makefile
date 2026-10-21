.PHONY: build test

build:
	go build -o bin/kitty-browse ./cmd/kitty-browse

test:
	go test ./...
