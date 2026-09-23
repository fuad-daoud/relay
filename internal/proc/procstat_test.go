package proc

import (
	"reflect"
	"testing"
)

func TestParseProcStatPPID(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want int
		ok   bool
	}{
		{
			name: "plain",
			stat: "123 (sleep) S 77 123 123 0 -1 4194560",
			want: 77,
			ok:   true,
		},
		{
			name: "command name with spaces and parentheses",
			stat: "123 (a) b) S 77 123 123 0 -1 4194560",
			want: 77,
			ok:   true,
		},
		{
			name: "command name ending in a parenthesis",
			stat: "123 (sh (worker)) R 77",
			want: 77,
			ok:   true,
		},
		{
			name: "no parentheses",
			stat: "123 sleep S 77",
			ok:   false,
		},
		{
			name: "nothing after the parentheses",
			stat: "123 (sleep)",
			ok:   false,
		},
		{
			name: "non-numeric ppid",
			stat: "123 (sleep) S x",
			ok:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseProcStatPPID(tc.stat)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("ppid = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParsePSChildren(t *testing.T) {
	out := `  100     1
  200   100
  300   100
  400     2

  500   10
`
	got := ParsePSChildren(out, 100)
	want := []int{200, 300}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePSChildren = %v, want %v", got, want)
	}
}
