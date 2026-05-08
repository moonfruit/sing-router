GO          ?= go
BIN         ?= sing-router
PKG         := github.com/moonfruit/sing-router/internal/version
VERSION     ?= 0.1.0+$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG).Version=$(VERSION)
CN_LIST_URL ?= https://cdn.jsdelivr.net/gh/juewuy/ShellCrash@update/bin/geodata/china_ip_list.txt

UPLOAD_DEST ?= /opt/bin/sing-router

.PHONY: build build-arm64 upload test cover fakebox update-cn

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sing-router

build-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)-linux-arm64 ./cmd/sing-router

upload: build-arm64
	./upload.sh -d $(UPLOAD_DEST) $(BIN)-linux-arm64

test:
	$(GO) test ./...

cover:
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out

fakebox:
	testdata/fake-sing-box/build.sh

update-cn:
	@rm -f assets/cn.txt.new
	@curl -fsSL --retry 3 \
	    --etag-compare assets/cn.txt.etag \
	    --etag-save    assets/cn.txt.etag \
	    -o assets/cn.txt.new "$(CN_LIST_URL)"
	@if [ -s assets/cn.txt.new ]; then \
	    mv assets/cn.txt.new assets/cn.txt; \
	    echo "assets/cn.txt updated ($$(wc -l < assets/cn.txt) lines)"; \
	else \
	    rm -f assets/cn.txt.new; \
	    echo "assets/cn.txt already up to date (HTTP 304)"; \
	fi
