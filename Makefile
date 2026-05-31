BINARY := kubectl-fixora
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/fixora/kubectl-fixora/internal/version.Version=$(VERSION)

.PHONY: build test lint install clean release

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/kubectl-fixora

test:
	go test ./...

lint:
	go test ./...

install: build
	install -m 0755 bin/$(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -rf bin dist

release:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_amd64/$(BINARY) ./cmd/kubectl-fixora
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_arm64/$(BINARY) ./cmd/kubectl-fixora
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_amd64/$(BINARY) ./cmd/kubectl-fixora
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_arm64/$(BINARY) ./cmd/kubectl-fixora
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_windows_amd64/$(BINARY).exe ./cmd/kubectl-fixora
