GO          ?= go
BIN         ?= sing-router
PKG         := github.com/moonfruit/sing-router/internal/version
VERSION     ?= 0.1.0+$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG).Version=$(VERSION)

.PHONY: build build-arm64 test cover fakebox

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sing-router

build-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)-linux-arm64 ./cmd/sing-router

test:
	$(GO) test ./...

cover:
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out

fakebox:
	testdata/fake-sing-box/build.sh
