VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

BUILDINFO := github.com/anovimous/automata-worker-monitor/internal/buildinfo
LDFLAGS   := -X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Commit=$(COMMIT)

# the binaries that exist so far; the others are added with their phase
CMDS := $(notdir $(wildcard cmd/*))

.PHONY: build build-noweb test vet lint web web-check images test-integration e2e clean

## build: the UI, then the binaries into bin/ with the UI embedded (build tag webui)
build: web
	CGO_ENABLED=0 go build -tags webui -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

## build-noweb: the binaries without the UI (no Node needed); the control plane serves a placeholder page
build-noweb:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

## test: unit tests with the race detector
test:
	go test -race ./...

vet:
	go vet ./...

## lint: staticcheck when it is installed
lint:
	@if command -v staticcheck >/dev/null; then staticcheck ./...; else echo "staticcheck is not installed, skipped"; fi

## web: the UI into web/dist
web:
	cd web && npm ci && npm run build

## web-check: lint and unit tests of the UI
web-check:
	cd web && npm ci && npm run lint && npm test

## images: one Docker image per Dockerfile.<binary>
images: $(addprefix image-,$(CMDS))

image-%:
	@if [ -f Dockerfile.$* ]; then \
		docker build -f Dockerfile.$* --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
			-t automata-$*:$(if $(filter fakeworker,$*),dev,$(VERSION)) . ; \
	else echo "no Dockerfile.$*, skipped"; fi

## test-integration: tests that need a Docker daemon
test-integration:
	go test -race -tags integration ./...

## e2e: the end-to-end scenarios (from P7)
e2e:
	test/e2e/run.sh

clean:
	rm -rf bin web/dist
