package main

import (
	"strings"
	"testing"
)

// TestEdgeAddRequiresFlags pins that `relay edge add` without a required
// flag -- or without the source positional -- is refused before a runtime is
// built, so a CI runner with no harness binary still fails on the missing flag
// rather than on the environment (mirrors cmdReview's --file check).
func TestEdgeAddRequiresFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			"missing source",
			[]string{"edge", "add", "--when", "report", "--then", "send", "--target", "client", "--prompt", "p.md"},
			"source binding name",
		},
		{
			"missing when",
			[]string{"edge", "add", "api", "--then", "send", "--target", "client", "--prompt", "p.md"},
			"--when",
		},
		{
			"missing then",
			[]string{"edge", "add", "api", "--when", "report", "--target", "client", "--prompt", "p.md"},
			"--then",
		},
		{
			"missing target",
			[]string{"edge", "add", "api", "--when", "report", "--then", "send", "--prompt", "p.md"},
			"--target",
		},
		{
			"missing prompt",
			[]string{"edge", "add", "api", "--when", "report", "--then", "send", "--target", "client"},
			"--prompt",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := run(c.args)
			if err == nil {
				t.Fatalf("relay %s: want an error", strings.Join(c.args, " "))
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
		})
	}
}
