GO        ?= go
GOBIN     ?= $(CURDIR)/bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X github.com/RefireLab/gh-runnerd/internal/version.Version=$(VERSION)
GOFLAGS   ?=

.PHONY: all build test vet fmt tidy ci guest runner-image deb dist clean

all: build

build: guest
	@mkdir -p $(GOBIN)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(GOBIN)/gh-runnerd ./cmd/gh-runnerd

guest:
	@mkdir -p $(GOBIN)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(GOBIN)/gh-runnerd-guest ./cmd/gh-runnerd-guest

test:
	$(GO) test $(GOFLAGS) ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

ci: vet test build

dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o dist/gh-runnerd-linux-amd64 ./cmd/gh-runnerd
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o dist/gh-runnerd-guest-linux-amd64 ./cmd/gh-runnerd-guest
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o dist/gh-runnerd-linux-arm64 ./cmd/gh-runnerd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o dist/gh-runnerd-guest-linux-arm64 ./cmd/gh-runnerd-guest

runner-image: guest
	./images/runner/bake.sh

deb: build
	./packaging/deb/build.sh

clean:
	rm -rf $(GOBIN) dist /tmp/gh-runnerd-deb-root
