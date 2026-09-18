package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// binExec is usage.Exec over the real PATH. Stderr rides on the error so
// the reader can quote its first line.
type binExec struct{}

func (binExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, errors.New(s)
		}
		return nil, err
	}
	return out, nil
}
