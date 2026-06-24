VERSION := $(shell cat VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := okboy
GO      ?= go

.PHONY: build static test vet fmt integration release clean

# Host build (dev).
build:
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/okboy

# Single static linux/amd64 binary. CGO_ENABLED=0 forces the pure-Go
# modernc.org/sqlite driver → no libc dependency, drops onto any Linux host.
static:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-linux-amd64 ./cmd/okboy

# Hermetic unit tests (run anywhere, incl. non-Linux dev — MockBackend, no nft/root).
test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Real-nftables integration test (Linux + root). Runs in an isolated network
# namespace so it never touches the host / k8s firewall.
integration:
	$(GO) test -tags integration -c -o /tmp/okboy-nfttest ./internal/firewall/
	sudo ip netns add okboy_it_ns 2>/dev/null || true
	sudo ip netns exec okboy_it_ns env PATH=/usr/sbin:/usr/bin:/bin /tmp/okboy-nfttest -test.run TestNftIntegration -test.v; \
		rc=$$?; sudo ip netns del okboy_it_ns 2>/dev/null || true; exit $$rc

# Release tarball: static binary + deploy assets + config example.
release: static
	tar -czf dist/$(BIN)-$(VERSION)-linux-amd64.tar.gz \
		-C dist $(BIN)-linux-amd64 \
		-C .. config.example.yaml deploy

clean:
	rm -rf bin dist
