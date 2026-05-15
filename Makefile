GO          ?= go
BIN         ?= sing-router
PKG         := github.com/moonfruit/sing-router/internal/version
VERSION     ?= 0.1.0+$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG).Version=$(VERSION)
CN_LIST_URL ?= https://cdn.jsdelivr.net/gh/juewuy/ShellCrash@update/bin/geodata/china_ip_list.txt

UPLOAD_DEST ?= /opt/sbin/sing-router

.PHONY: build build-arm64 upload test cover fakebox update-cn update-rule-sets update-all docker-test

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

docker-test:
	@tests/docker/docker-test.sh

update-cn:
	@scripts/fetch-asset.sh "$(CN_LIST_URL)" assets/var/cn.txt

# 4 个 sing-box 内嵌 rule_set；token 从 sops 加密的 secrets 解出。
RULE_SETS    ?= geoip-cn.srs geosites-cn.srs lan.srs fakeip-bypass.srs
GITEE_OWNER  ?= moonfruit
GITEE_REPO   ?= private
GITEE_REF    ?= main

update-rule-sets:
	@token=$$(sops -d secrets/sing-router.env | grep '^SING_ROUTER_GITEE_TOKEN=' | cut -d= -f2-) && \
	    if [ -z "$$token" ]; then echo "ERROR: SING_ROUTER_GITEE_TOKEN not in secrets/sing-router.env" >&2; exit 1; fi && \
	    for f in $(RULE_SETS); do \
	        url="https://gitee.com/api/v5/repos/$(GITEE_OWNER)/$(GITEE_REPO)/raw/rules/$$f?ref=$(GITEE_REF)&access_token=$$token"; \
	        scripts/fetch-asset.sh "$$url" "assets/rules/$$f"; \
	    done

update-all: update-cn update-rule-sets
	@echo "all embedded assets up to date"

.PHONY: realdevice-lint realdevice-test
# 实机测试套件：lint 跑纯逻辑单测 + 用例语法检查（无需路由器）
realdevice-lint:
	bash tests/realdevice/lib/probe_test.sh
	bash tests/realdevice/lib/harness_test.sh
	bash tests/realdevice/run.sh --dry-run
# 跑实机用例（需 tests/realdevice/config.sh + 可达路由器）；CASES 可选，如 `make realdevice-test CASES="S W"`
realdevice-test:
	bash tests/realdevice/run.sh $(CASES)
