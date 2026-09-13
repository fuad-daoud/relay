package relay

import (
	"testing"
	"time"
)

func TestAgeText(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h 0m"},
		{27*time.Hour + 4*time.Minute + 30*time.Second, "27h 4m"},
		{-5 * time.Second, "0s"},
	}

	for _, tt := range tests {
		got := AgeText(tt.input)
		if got != tt.want {
			t.Errorf("AgeText(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
