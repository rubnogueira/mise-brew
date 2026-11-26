PREFIX ?= $(shell ./bin/brew --prefix 2>/dev/null || echo /opt/homebrew)

.PHONY: build test install clean
build:
	go build -trimpath -ldflags='-s -w' -o bin/brew ./cmd/brew
test:
	go vet ./...
	go test ./...
install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/brew $(PREFIX)/bin/brew
clean:
	rm -rf bin
