package mcp

import "testing"

func TestDetectMode(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want Mode
	}{
		{
			name: "dangerously-load-development-channels",
			argv: []string{"claude", "--agent", "architect", "--dangerously-load-development-channels", "plugin:relevo@relevo"},
			want: ModeChannel,
		},
		{
			name: "channels with equals",
			argv: []string{"claude", "--channels=plugin:x"},
			want: ModeChannel,
		},
		{
			name: "channels exact",
			argv: []string{"claude", "--channels"},
			want: ModeChannel,
		},
		{
			name: "plain -p",
			argv: []string{"claude", "-p", "hi"},
			want: ModeTools,
		},
		{
			name: "empty argv",
			argv: nil,
			want: ModeTools,
		},
		{
			name: "unrelated flag containing the substring",
			argv: []string{"claude", "--channelsfoo"},
			want: ModeTools,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectMode(tt.argv); got != tt.want {
				t.Errorf("DetectMode(%v) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}
