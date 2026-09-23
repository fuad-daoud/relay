Implemented the JSON response serializer for the status endpoint.

Added tests validating that timestamp formatting matches ISO-8601 UTC.

Verified that memory allocations during serialization remain zero in benchmarks.

```relevo
status: done
changed_paths: ["internal/api/status.go", "internal/api/status_test.go"]
commands_run: ["go test ./internal/api"]
not_done: []
```
