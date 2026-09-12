package pick

import "testing"

// TestListWindow pins the window rule copied from internal/ui/list.go: the
// cursor is always visible and the top moves as little as possible.
func TestListWindow(t *testing.T) {
	cases := []struct {
		name                 string
		top, cursor, rows, n int
		want                 int
	}{
		{"no budget", 3, 5, 0, 10, 0},
		{"everything fits", 3, 5, 10, 8, 0},
		{"cursor inside window", 2, 4, 3, 10, 2},
		{"cursor above window", 5, 2, 3, 10, 2},
		{"cursor below window", 0, 6, 3, 10, 4},
		{"top past the end is clamped", 9, 9, 3, 10, 7},
	}
	for _, c := range cases {
		if got := listWindow(c.top, c.cursor, c.rows, c.n); got != c.want {
			t.Errorf("%s: listWindow(%d,%d,%d,%d) = %d, want %d", c.name, c.top, c.cursor, c.rows, c.n, got, c.want)
		}
	}
}

func TestRenderRowMarksTheSelection(t *testing.T) {
	r := row("webshop", "NEEDS YOU", "blocked")
	sel := renderRow(r, true)
	unsel := renderRow(r, false)
	if sel == unsel {
		t.Fatal("selected and unselected rows render identically")
	}
	for _, want := range []string{"webshop", "NEEDS YOU", "r2", "agy", "blocked"} {
		if !contains(unsel, want) {
			t.Errorf("row %q lacks %q", unsel, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
