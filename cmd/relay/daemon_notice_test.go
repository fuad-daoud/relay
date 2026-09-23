package main

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDaemonNotice(t *testing.T) {
	const predates = "relay daemon predates version tracking -- restart it once (relay doctor)"
	const refused = "relay daemon refused the new binary -- relay doctor"

	tests := []struct {
		name    string
		cli     string
		info    store.DaemonInfo
		ok      bool
		running bool
		want    string
	}{
		{
			name:    "not running prints nothing",
			cli:     "v2",
			ok:      false,
			running: false,
			want:    "",
		},
		{
			name:    "missing daemon.json while running says it predates tracking",
			cli:     "v2",
			ok:      false,
			running: true,
			want:    predates,
		},
		{
			name:    "a refused binary says so",
			cli:     "v2",
			info:    store.DaemonInfo{Version: "v1", ReexecFailed: &store.ReexecFailure{Reason: "boom"}},
			ok:      true,
			running: true,
			want:    refused,
		},
		{
			name:    "a plain version difference prints nothing",
			cli:     "v2",
			info:    store.DaemonInfo{Version: "v1"},
			ok:      true,
			running: true,
			want:    "",
		},
		{
			name:    "equal versions print nothing",
			cli:     "v2",
			info:    store.DaemonInfo{Version: "v2"},
			ok:      true,
			running: true,
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonNotice(tc.cli, tc.info, tc.ok, tc.running); got != tc.want {
				t.Errorf("daemonNotice(%q, %+v, %v, %v) = %q, want %q",
					tc.cli, tc.info, tc.ok, tc.running, got, tc.want)
			}
		})
	}
}
