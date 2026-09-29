GO        ?= go
BIN       ?= bin/pagerelic
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/shanurwan/pagerelic/internal/buildinfo.Version=$(VERSION)
FUZZTIME  ?= 30s
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test race cover vet fmt-check lint vuln fuzz cross check clean

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/pagerelic

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

# Every parser of on-disk bytes.
fuzz:
	$(GO) test ./internal/page    -run '^$$' -fuzz '^FuzzHeaderAndItems$$' -fuzztime $(FUZZTIME)
	$(GO) test ./internal/heap    -run '^$$' -fuzz '^FuzzParseHeader$$'    -fuzztime $(FUZZTIME)
	$(GO) test ./internal/datum   -run '^$$' -fuzz '^FuzzDecompress$$'     -fuzztime $(FUZZTIME)
	$(GO) test ./internal/datum   -run '^$$' -fuzz '^FuzzFormat$$'         -fuzztime $(FUZZTIME)
	$(GO) test ./internal/recover -run '^$$' -fuzz '^FuzzScanPage$$'       -fuzztime $(FUZZTIME)
	$(GO) test ./internal/carve   -run '^$$' -fuzz '^FuzzExamine$$'        -fuzztime $(FUZZTIME)

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$p"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/pagerelic-$$os-$$arch$$ext ./cmd/pagerelic || exit 1; \
	done
	cd dist && sha256sum pagerelic-* > SHA256SUMS

check: fmt-check vet test

clean:
	rm -rf bin dist coverage.out
