Configured the TLS listener certificates in internal/server/tls.go.

The documentation notes that the config says you must set X before Y; I set it accordingly in the initialization sequence. This prevents handshake negotiation errors on startup.

The integration tests verified valid handshakes across all supported clients.

```relay
status: done
changed_paths: ["internal/server/tls.go", "internal/server/tls_test.go"]
commands_run: ["go test ./internal/server"]
not_done: []
```
