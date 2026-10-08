PANEL_REPOSITORY ?= router-for-me/Home-Management-Center
PANEL_WORKDIR ?= $(CURDIR)/.tmp/home-management-center
EMBED_STATIC_DIR := internal/managementasset/static
LOCAL_BINARY ?= CLIProxyAPIHome
DOCKER_IMAGE ?= cliproxyapi-home:embedded-local
GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt lint embed-local panel-assets-local docker-embed-local run-embedded-local clean-embedded-local

# Format Go sources in module packages (skips dot dirs such as .tmp and testdata).
fmt:
	gofmt -w $$(go list -f '{{.Dir}}' ./...)

lint:
	@command -v "$(GOLANGCI_LINT)" >/dev/null || { echo "golangci-lint is required: https://golangci-lint.run/welcome/install/"; exit 1; }
	@test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))" || { echo "gofmt needed on:"; gofmt -l $$(go list -f '{{.Dir}}' ./...); exit 1; }
	"$(GOLANGCI_LINT)" run ./...

# Local-only helper. GitHub Actions builds embedded assets independently.
embed-local: panel-assets-local
	go build -o "$(LOCAL_BINARY)" ./cmd/home

docker-embed-local: panel-assets-local
	docker build -t "$(DOCKER_IMAGE)" .

panel-assets-local:
	@command -v gh >/dev/null || { echo "gh is required"; exit 1; }
	@command -v bun >/dev/null || { echo "bun is required"; exit 1; }
	rm -rf "$(PANEL_WORKDIR)"
	mkdir -p "$(dir $(PANEL_WORKDIR))"
	gh repo clone "$(PANEL_REPOSITORY)" "$(PANEL_WORKDIR)" -- --depth 1
	cd "$(PANEL_WORKDIR)" && bun install --frozen-lockfile && bun run build:embedded
	mkdir -p "$(EMBED_STATIC_DIR)"
	touch "$(EMBED_STATIC_DIR)/.gitkeep"
	find "$(EMBED_STATIC_DIR)" -mindepth 1 ! -name ".gitkeep" -exec rm -rf {} +
	cp -R "$(PANEL_WORKDIR)/dist/." "$(EMBED_STATIC_DIR)/"

run-embedded-local: panel-assets-local
	go run ./cmd/home

clean-embedded-local:
	rm -rf "$(PANEL_WORKDIR)"
	rm -rf "$(EMBED_STATIC_DIR)"
	mkdir -p "$(EMBED_STATIC_DIR)"
	touch "$(EMBED_STATIC_DIR)/.gitkeep"
