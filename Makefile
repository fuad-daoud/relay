BIN     := $(HOME)/.local/bin/relay
UNIT    := $(HOME)/.config/systemd/user/relay.service
LABEL   := com.github.fuad-daoud.relay
PLIST   := $(HOME)/Library/LaunchAgents/$(LABEL).plist
UNAME_S := $(shell uname -s)

# Stamp the binary so `relay version` means something in a build made from a
# clone. A `go install`ed binary gets its version from the module proxy
# instead, so this is only needed here.
BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
LDFLAGS := -X main.version=$(if $(VERSION),$(VERSION),$(BUILD_VERSION))

.PHONY: check build install service uninstall release jev

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go test -race -count=1 ./...
	@cp go.mod go.mod.check && cp go.sum go.sum.check && \
	if ! go mod tidy || ! cmp -s go.mod go.mod.check || ! cmp -s go.sum go.sum.check; then \
		mv go.mod.check go.mod && mv go.sum.check go.sum; \
		echo "go.mod or go.sum is not tidy; run 'go mod tidy'"; \
		exit 1; \
	fi; \
	rm -f go.mod.check go.sum.check
	sh scripts/check-plugin-version.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping shell lint"; \
	fi
	@for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t" || exit 1; done

# e2e runs one relay round against a private, detached herdr session with
# scripted agents (docs/specs/2026-09-12-e2e-real-herdr-design.md). Local
# only: it needs herdr on PATH and skips otherwise. Not part of check.
e2e:
	go vet -tags e2e ./internal/relay
	go test -tags e2e -count=1 -run TestE2E ./internal/relay -v

# jev runs the classifier fixtures against the real TypeSafe endpoint
# (docs/plans/2026-09-19-injection-classify.md §8). Local only: it needs
# TYPESAFE_API_KEY or ~/.config/relay/typesafe.key and skips otherwise.
# Not part of check.
jev:
	go vet -tags jev ./internal/classify
	go test -tags jev -count=1 -run TestJevInjectionFixtures ./internal/classify -v

build: check
	go build -ldflags "$(LDFLAGS)" -o relay ./cmd/relay

release:
	@test -n "$(VERSION)" || { echo "VERSION is required (e.g. make release VERSION=0.1.0)" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty" >&2; exit 1; }
	@test "$$(git branch --show-current)" = "main" || { echo "not on main branch" >&2; exit 1; }
	sed 's/^version = ".*"/version = "$(VERSION)"/' herdr-plugin.toml > herdr-plugin.toml.tmp && mv herdr-plugin.toml.tmp herdr-plugin.toml
	sed 's/^version = ".*"/version = "$(VERSION)"/' from-source/herdr-plugin.toml > from-source/herdr-plugin.toml.tmp && mv from-source/herdr-plugin.toml.tmp from-source/herdr-plugin.toml
	sed 's|<span data-version>v[^<]*</span>|<span data-version>v$(VERSION)</span>|' web/index.html > web/index.html.tmp && mv web/index.html.tmp web/index.html
	$(MAKE) check
	git commit -m "chore(release): v$(VERSION)" herdr-plugin.toml from-source/herdr-plugin.toml web/index.html
	git tag -a v$(VERSION) -m "v$(VERSION)"
	@echo "git push && git push origin v$(VERSION)"

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
	systemctl --user enable relay.service
	systemctl --user restart relay.service

uninstall:
	systemctl --user disable --now relay.service || true
	rm -f $(BIN) $(UNIT)
	systemctl --user daemon-reload

endif
