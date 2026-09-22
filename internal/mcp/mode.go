package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// channelFlags are the argv elements that mean Claude Code loaded this
// server under a channel-capable session (spec §7).
var channelFlags = []string{"--channels", "--dangerously-load-development-channels"}

// DetectMode reports ModeChannel iff parentArgv contains one of
// channelFlags exactly, or an element that starts with one of them
// followed by "=" (e.g. "--channels=plugin:x"). Anything else -> ModeTools.
// Pure: it looks at nothing but its argument.
func DetectMode(parentArgv []string) Mode {
	for _, arg := range parentArgv {
		for _, flag := range channelFlags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return ModeChannel
			}
		}
	}
	return ModeTools
}

// ParentArgv returns this process's parent's command line: on Linux,
// /proc/<ppid>/cmdline, NUL-separated; on macOS, `ps -o args= -p <ppid>`,
// space-separated. Any other platform, or a parent relay cannot read,
// returns an error -- DetectMode's caller then falls back to ModeTools and
// logs why (spec §7 risk: mode detection fails closed).
func ParentArgv() ([]string, error) {
	ppid := os.Getppid()

	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", ppid))
		if err != nil {
			return nil, fmt.Errorf("read parent cmdline: %w", err)
		}
		parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(parts) == 1 && parts[0] == "" {
			return nil, fmt.Errorf("empty parent cmdline for pid %d", ppid)
		}
		return parts, nil

	case "darwin":
		out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(ppid)).Output()
		if err != nil {
			return nil, fmt.Errorf("ps parent args: %w", err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty parent args for pid %d", ppid)
		}
		return fields, nil

	default:
		return nil, fmt.Errorf("parent argv detection is not supported on %s", runtime.GOOS)
	}
}
