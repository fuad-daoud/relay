package git

import (
	"testing"
)

func TestStatEmpty(t *testing.T) {
	tests := []struct {
		name string
		stat Stat
		want bool
	}{
		{
			name: "zero value stat is empty",
			stat: Stat{},
			want: true,
		},
		{
			name: "stat with zero files changed is empty",
			stat: Stat{FilesChanged: 0, Insertions: 5, Deletions: 2},
			want: true,
		},
		{
			name: "stat with non-zero files changed is not empty",
			stat: Stat{FilesChanged: 1, Insertions: 0, Deletions: 0},
			want: false,
		},
		{
			name: "stat with non-zero files changed and changes is not empty",
			stat: Stat{FilesChanged: 3, Insertions: 10, Deletions: 4},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stat.Empty(); got != tt.want {
				t.Errorf("Stat.Empty() = %v, want %v", got, tt.want)
			}
		})
	}
}
