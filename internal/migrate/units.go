package migrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

// ClientUnits is the pair of user-level client services `relevo migrate`
// swaps: the relay-era unit and the relevo-era unit it becomes. Platform is
// the GOOS they belong to; a platform with no units leaves both zero, so every
// function here is a no-op on it.
type ClientUnits struct {
	Platform string // "linux" | "darwin"; anything else: no units
	Old, New Unit
}

// OldUnit is what StopOld found about the old client service and what it did.
type OldUnit struct {
	Installed  bool
	WasActive  bool
	WasEnabled bool
}

// UnitOptions is everything the unit steps need: the units themselves, the
// service manager, the embedded unit texts, where relevo runs, and where the
// report goes.
type UnitOptions struct {
	Units          ClientUnits
	Services       Services
	ClientUnitText string // dist.ClientUnit
	PlistTemplate  string // dist.LaunchdPlist (@BIN@, @HOME@)
	Exe, Home      string // running executable (resolved), $HOME
	DryRun         bool
	Out            io.Writer
}

// DefaultClientUnits resolves the old and the new client unit paths for goos.
// configHome is $XDG_CONFIG_HOME, where systemd keeps user units; home is
// $HOME, where launchd keeps LaunchAgents. Every relay-era spelling comes from
// internal/legacy, the only home of the old names.
func DefaultClientUnits(goos, configHome, home string) ClientUnits {
	switch goos {
	case "linux":
		return ClientUnits{
			Platform: "linux",
			Old: Unit{
				Name: legacy.ClientUnit,
				Path: filepath.Join(configHome, "systemd", "user", legacy.ClientUnit),
			},
			New: Unit{
				Name: "relevo.service",
				Path: filepath.Join(configHome, "systemd", "user", "relevo.service"),
			},
		}
	case "darwin":
		oldLabel := legacy.LaunchdLabel
		newLabel := "com.github.fuad-daoud.relevo"
		return ClientUnits{
			Platform: "darwin",
			Old: Unit{
				Name: oldLabel,
				Path: filepath.Join(home, "Library", "LaunchAgents", oldLabel+".plist"),
			},
			New: Unit{
				Name: newLabel,
				Path: filepath.Join(home, "Library", "LaunchAgents", newLabel+".plist"),
			},
		}
	default:
		return ClientUnits{Platform: goos}
	}
}

// step records one line of the unit report and writes it, with the same
// dry-run prefix Run uses so a report cannot be mistaken for work done.
func (o UnitOptions) step(name, detail string, skipped bool) Step {
	s := Step{Name: name, Detail: detail, Skipped: skipped}
	if o.Out != nil {
		prefix := ""
		if o.DryRun {
			prefix = "(dry run) "
		}
		fmt.Fprintf(o.Out, "%s%s: %s\n", prefix, s.Name, s.Detail)
	}
	return s
}

// newUnitContent renders the new client unit: dist.ClientUnit on Linux, the
// plist template with @BIN@ and @HOME@ substituted on macOS.
func (o UnitOptions) newUnitContent() ([]byte, error) {
	switch o.Units.Platform {
	case "linux":
		return []byte(o.ClientUnitText), nil
	case "darwin":
		s := strings.ReplaceAll(o.PlistTemplate, "@BIN@", o.Exe)
		s = strings.ReplaceAll(s, "@HOME@", o.Home)
		return []byte(s), nil
	default:
		return nil, fmt.Errorf("migrate: no client unit for platform %q", o.Units.Platform)
	}
}

// StopOld stops the old client service when it is installed and running, and
// reports what it found. An installed but inactive unit is left alone: a host
// with an installed-but-inactive, disabled relay.service (contabo) must not
// get a relevo daemon nobody asked for. An absent unit file is a skipped step
// and a zero OldUnit.
//
// A dry run makes no Services call at all -- the report describes the stop it
// would make, and WasActive/WasEnabled stay false because they cannot be known
// without asking the service manager.
func StopOld(ctx context.Context, o UnitOptions) (Step, OldUnit, error) {
	var old OldUnit

	if _, err := os.Stat(o.Units.Old.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return o.step("stop", "no client unit was installed", true), old, nil
		}
		return o.step("stop", fmt.Sprintf("stat %s: %v", o.Units.Old.Path, err), false), old, err
	}
	old.Installed = true

	if o.DryRun {
		return o.step("stop", fmt.Sprintf("stop %s if it is running", o.Units.Old.Name), false), old, nil
	}

	active, enabled, err := o.Services.State(ctx, o.Units.Old)
	if err != nil {
		return o.step("stop", fmt.Sprintf("read the state of %s: %v", o.Units.Old.Name, err), false), old, err
	}
	old.WasActive, old.WasEnabled = active, enabled

	if !active {
		return o.step("stop", fmt.Sprintf("%s is installed, not running", o.Units.Old.Name), true), old, nil
	}
	if err := o.Services.Stop(ctx, o.Units.Old); err != nil {
		return o.step("stop", fmt.Sprintf("stop %s: %v", o.Units.Old.Name, err), false), old, err
	}
	return o.step("stop", fmt.Sprintf("stopped %s", o.Units.Old.Name), false), old, nil
}

// SwapClient installs the new client unit and retires the old one, in that
// order, and returns one step per thing it did. With no old unit installed it
// is a single skipped step and touches nothing: relevo must not install a
// daemon on a host that never ran one.
//
// The new unit is enabled only when the old one was active or enabled; a
// migration preserves the old host's intent rather than inventing one. The
// install is idempotent: a unit file already equal to what would be written is
// not rewritten, and an absent old file skips the retire.
func SwapClient(ctx context.Context, o UnitOptions, old OldUnit) ([]Step, error) {
	if !old.Installed {
		return []Step{o.step("install", "no client unit was installed", true)}, nil
	}

	var steps []Step

	// Install.
	content, err := o.newUnitContent()
	if err != nil {
		return steps, err
	}
	current, readErr := os.ReadFile(o.Units.New.Path)
	unchanged := readErr == nil && bytes.Equal(current, content)

	if o.DryRun {
		detail := fmt.Sprintf("write %s", o.Units.New.Path)
		if unchanged {
			detail = fmt.Sprintf("%s is already up to date", o.Units.New.Path)
		}
		if old.WasActive || old.WasEnabled {
			detail += "; enable and start it"
		} else {
			detail += "; enable and start it if the old unit was active or enabled"
		}
		steps = append(steps, o.step("install", detail, unchanged))
	} else {
		if !unchanged {
			if err := os.MkdirAll(filepath.Dir(o.Units.New.Path), 0o755); err != nil {
				return steps, fmt.Errorf("install: %w", err)
			}
			if err := os.WriteFile(o.Units.New.Path, content, 0o644); err != nil {
				return steps, fmt.Errorf("install: %w", err)
			}
			if o.Units.Platform == "linux" {
				if err := o.Services.Reload(ctx); err != nil {
					return steps, fmt.Errorf("install: %w", err)
				}
			}
		}

		if old.WasActive || old.WasEnabled {
			if err := o.Services.Enable(ctx, o.Units.New); err != nil {
				return steps, fmt.Errorf("install: %w", err)
			}
			steps = append(steps, o.step("install",
				fmt.Sprintf("installed and enabled %s", o.Units.New.Name), false))
		} else {
			steps = append(steps, o.step("install",
				fmt.Sprintf("installed %s, left disabled like the old unit", o.Units.New.Name), false))
		}
	}

	// Retire: an absent old file skips it.
	if _, err := os.Stat(o.Units.Old.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return steps, nil
		}
		return steps, fmt.Errorf("retire: stat %s: %w", o.Units.Old.Path, err)
	}

	if o.DryRun {
		steps = append(steps, o.step("retire",
			fmt.Sprintf("disable and remove %s", o.Units.Old.Path), false))
		return steps, nil
	}

	if err := o.Services.Disable(ctx, o.Units.Old); err != nil {
		return steps, fmt.Errorf("retire: %w", err)
	}
	if err := os.Remove(o.Units.Old.Path); err != nil {
		return steps, fmt.Errorf("retire: %w", err)
	}
	if o.Units.Platform == "linux" {
		if err := o.Services.Reload(ctx); err != nil {
			return steps, fmt.Errorf("retire: %w", err)
		}
	}
	steps = append(steps, o.step("retire",
		fmt.Sprintf("disabled and removed %s", o.Units.Old.Path), false))
	return steps, nil
}

// RemoveOldBinary removes the old `relay` binary that sits beside the running
// relevo, unless --keep-old-binary is given. A sibling that is absent, is not
// a regular file, or is the running binary itself is a skipped step. A dry run
// reports the removal without making it.
func RemoveOldBinary(exe string, keep, dryRun bool) (Step, error) {
	sibling := filepath.Join(filepath.Dir(exe), legacy.Binary)

	fi, err := os.Stat(sibling)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Step{Name: "old-binary", Detail: fmt.Sprintf("no %s beside relevo", legacy.Binary), Skipped: true}, nil
		}
		return Step{Name: "old-binary", Detail: fmt.Sprintf("stat %s: %v", sibling, err)}, err
	}
	if !fi.Mode().IsRegular() {
		return Step{Name: "old-binary", Detail: fmt.Sprintf("%s is not a regular file", sibling), Skipped: true}, nil
	}
	if exeInfo, err := os.Stat(exe); err == nil && os.SameFile(exeInfo, fi) {
		return Step{Name: "old-binary", Detail: fmt.Sprintf("%s is this binary", sibling), Skipped: true}, nil
	}
	if keep {
		return Step{Name: "old-binary", Detail: fmt.Sprintf("%s kept (--keep-old-binary)", sibling), Skipped: true}, nil
	}
	if dryRun {
		return Step{Name: "old-binary", Detail: fmt.Sprintf("remove %s", sibling)}, nil
	}
	if err := os.Remove(sibling); err != nil {
		return Step{Name: "old-binary", Detail: fmt.Sprintf("remove %s: %v", sibling, err)}, err
	}
	return Step{Name: "old-binary", Detail: fmt.Sprintf("removed %s", sibling)}, nil
}

// RenameSliceValue replaces the exact JSON string value `"relay.slice"` with
// `"relevo.slice"` in <configDir>/policy.json. serve.scope.slice holds
// "relay.slice" on the laptop and on contabo, where the old 6G slice is
// retired and relevo.slice replaces it (#292 §3 step 6).
//
// Only the quoted value changes, so `"relay.slicer"` is left alone. The write
// is atomic and keeps the file's mode; an unchanged or absent file is
// untouched, mtime included. A dry run reports the step only.
func RenameSliceValue(configDir string, dryRun bool) (Step, error) {
	path := filepath.Join(configDir, "policy.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Step{Name: "rename-slice", Detail: "no policy.json", Skipped: true}, nil
		}
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("read %s: %v", path, err)}, err
	}

	oldSeq := []byte(`"` + legacy.Slice + `"`) // "relay.slice"
	newSeq := []byte(`"relevo.slice"`)
	if !bytes.Contains(data, oldSeq) {
		return Step{Name: "rename-slice", Detail: "no relay.slice in policy.json", Skipped: true}, nil
	}

	if dryRun {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("rename %s to relevo.slice in %s", legacy.Slice, path)}, nil
	}

	fi, err := os.Stat(path)
	if err != nil {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("stat %s: %v", path, err)}, err
	}
	out := bytes.ReplaceAll(data, oldSeq, newSeq)
	if err := writeFileAtomic(path, out, fi.Mode().Perm()); err != nil {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("write %s: %v", path, err)}, err
	}
	return Step{Name: "rename-slice", Detail: fmt.Sprintf("renamed slice in %s", path)}, nil
}

// writeFileAtomic replaces path with data through a temp file in the same
// directory, so a crash cannot truncate the record it rewrites.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
