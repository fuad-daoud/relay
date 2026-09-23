package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/fuad-daoud/relay/internal/proc"
)

// lineExec is relay.LineExec's production implementation: one harness
// invocation, every stdout line handed to onLine as it arrives.
type lineExec struct{}

// Run starts argv in dir with the parent environment minus proc.DeniedEnv,
// calls onLine for every stdout line, and returns a non-zero exit as an error
// whose text ends with the last 300 bytes of stderr.
func (lineExec) Run(ctx context.Context, dir string, argv []string, onLine func(line []byte)) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = proc.ChildEnv(os.Environ(), proc.DeniedEnv, nil)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if onLine != nil {
			onLine(sc.Bytes())
		}
	}
	if err := cmd.Wait(); err != nil {
		tail := stderr.Bytes()
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return fmt.Errorf("%w: %s", err, tail)
	}
	return nil
}
