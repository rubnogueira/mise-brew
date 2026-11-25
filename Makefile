PREFIX ?= /opt/homebrew

.PHONY: build test install clean
build:
	go build -trimpath -ldflags='-s -w' -o bin/brew .
test:
	go test ./...
install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/brew $(PREFIX)/bin/brew
clean:
	rm -rf bin
