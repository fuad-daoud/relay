BIN     := $(HOME)/.local/bin/relay
UNIT    := $(HOME)/.config/systemd/user/relay.service
LABEL   := com.github.fuad-daoud.relay
PLIST   := $(HOME)/Library/LaunchAgents/$(LABEL).plist
UNAME_S := $(shell uname -s)

# Stamp the binary so `relay version` means something in a build made from a
# clone. A `go install`ed binary gets its version from the module proxy
# instead, so this is only needed here.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: check build install service uninstall

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go test -count=1 ./...
	@cp go.mod go.mod.check && cp go.sum go.sum.check && \
	if ! go mod tidy || ! cmp -s go.mod go.mod.check || ! cmp -s go.sum go.sum.check; then \
		mv go.mod.check go.mod && mv go.sum.check go.sum; \
		echo "go.mod or go.sum is not tidy; run 'go mod tidy'"; \
		exit 1; \
	fi; \
	rm -f go.mod.check go.sum.check
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping shell lint"; \
	fi
	@for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t"; done

build: check
	go build -ldflags "$(LDFLAGS)" -o relay ./cmd/relay

# mkdir + install rather than `install -D`: -D is a GNU extension and the
# install(1) that ships with macOS does not have it.
install: build
	mkdir -p $(dir $(BIN))
	install -m755 relay $(BIN)

ifeq ($(UNAME_S),Darwin)

service: install
	mkdir -p $(dir $(PLIST))
	sed -e 's|@BIN@|$(BIN)|g' -e 's|@HOME@|$(HOME)|g' dist/$(LABEL).plist.in > $(PLIST)
	launchctl unload $(PLIST) 2>/dev/null || true
	launchctl load -w $(PLIST)

uninstall:
	launchctl unload $(PLIST) 2>/dev/null || true
	rm -f $(BIN) $(PLIST)

else

service: install
	install -Dm644 dist/relay.service $(UNIT)
	systemctl --user daemon-reload
	systemctl --user enable --now relay.service

uninstall:
	systemctl --user disable --now relay.service || true
	rm -f $(BIN) $(UNIT)
	systemctl --user daemon-reload

endif
