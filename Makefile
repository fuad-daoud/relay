BIN := $(HOME)/.local/bin/relay
UNIT := $(HOME)/.config/systemd/user/relay.service

.PHONY: check build install service uninstall

check:
	gofmt -l .
	go vet ./...
	go test -count=1 ./...

build: check
	go build -o relay ./cmd/relay

install: build
	install -Dm755 relay $(BIN)

service: install
	install -Dm644 dist/relay.service $(UNIT)
	systemctl --user daemon-reload
	systemctl --user enable --now relay.service

uninstall:
	systemctl --user disable --now relay.service || true
	rm -f $(BIN) $(UNIT)
	systemctl --user daemon-reload
