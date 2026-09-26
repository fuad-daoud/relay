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

// ClientUnits is the pair of client services `relevo migrate` swaps. A
// platform with no units leaves both zero.
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

// UnitOptions is everything the unit steps need.
type UnitOptions struct {
	Units          ClientUnits
	Services       Services
	ClientUnitText string // dist.ClientUnit
	PlistTemplate  string // dist.LaunchdPlist (@BIN@, @HOME@)
	Exe, Home      string // running executable (resolved), $HOME
	DryRun         bool
	Out            io.Writer
}

// DefaultClientUnits resolves the old and new client unit paths for goos:
// configHome for systemd, home for launchd.
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

// step records one line of the unit report, dry-run prefixed like addStep.
func (o UnitOptions) step(name, detail string, skipped bool) Step {
	s := Step{Name: name, Detail: detail, Skipped: skipped}
	if o.Out != nil {
		prefix := ""
		if o.DryRun {
			prefix = "(dry run) "
		}
		_, _ = fmt.Fprintf(o.Out, "%s%s: %s\n", prefix, s.Name, s.Detail)
	}
	return s
}

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

// StopOld stops the old client service when installed and running. An
// installed but inactive unit is left alone, and a dry run never calls
// Services at all.
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

// SwapClient installs the new client unit and retires the old one. With no
// old unit installed it is a single skipped step.
func SwapClient(ctx context.Context, o UnitOptions, old OldUnit) ([]Step, error) {
	if !old.Installed {
		return []Step{o.step("install", "no client unit was installed", true)}, nil
	}

	steps, err := installClientUnit(ctx, o, old)
	if err != nil {
		return steps, err
	}
	retireSteps, err := retireOldUnit(ctx, o)
	return append(steps, retireSteps...), err
}

// installClientUnit writes the new unit, skipping an already-equal file, and
// enables it only when the old one was active or enabled.
func installClientUnit(ctx context.Context, o UnitOptions, old OldUnit) ([]Step, error) {
	content, err := o.newUnitContent()
	if err != nil {
		return nil, err
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
		return []Step{o.step("install", detail, unchanged)}, nil
	}

	if !unchanged {
		if err := os.MkdirAll(filepath.Dir(o.Units.New.Path), 0o755); err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
		if err := os.WriteFile(o.Units.New.Path, content, 0o644); err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
		if o.Units.Platform == "linux" {
			if err := o.Services.Reload(ctx); err != nil {
				return nil, fmt.Errorf("install: %w", err)
			}
		}
	}

	if old.WasActive || old.WasEnabled {
		if err := o.Services.Enable(ctx, o.Units.New); err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
		return []Step{o.step("install", fmt.Sprintf("installed and enabled %s", o.Units.New.Name), false)}, nil
	}
	return []Step{o.step("install",
		fmt.Sprintf("installed %s, left disabled like the old unit", o.Units.New.Name), false)}, nil
}

// retireOldUnit disables and removes the old unit file, if any.
func retireOldUnit(ctx context.Context, o UnitOptions) ([]Step, error) {
	if _, err := os.Stat(o.Units.Old.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("retire: stat %s: %w", o.Units.Old.Path, err)
	}

	if o.DryRun {
		return []Step{o.step("retire", fmt.Sprintf("disable and remove %s", o.Units.Old.Path), false)}, nil
	}

	if err := o.Services.Disable(ctx, o.Units.Old); err != nil {
		return nil, fmt.Errorf("retire: %w", err)
	}
	if err := os.Remove(o.Units.Old.Path); err != nil {
		return nil, fmt.Errorf("retire: %w", err)
	}
	if o.Units.Platform == "linux" {
		if err := o.Services.Reload(ctx); err != nil {
			return nil, fmt.Errorf("retire: %w", err)
		}
	}
	return []Step{o.step("retire", fmt.Sprintf("disabled and removed %s", o.Units.Old.Path), false)}, nil
}

// RemoveOldBinary removes the old `relay` binary beside the running relevo, // name-guard: legacy
// unless --keep-old-binary is given. An absent, non-regular, or self-same
// sibling is a skipped step.
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

// RenameSliceValue replaces the exact quoted value `"relay.slice"` with // name-guard: legacy
// `"relevo.slice"` in <configDir>/policy.json (so `"relay.slicer"` is left // name-guard: legacy
// alone), atomically and only when the value is present.
func RenameSliceValue(configDir string, dryRun bool) (Step, error) {
	path := filepath.Join(configDir, "policy.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Step{Name: "rename-slice", Detail: "no policy.json", Skipped: true}, nil
		}
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("read %s: %v", path, err)}, err
	}

	if !bytes.Contains(data, legacySliceSeq) {
		return Step{Name: "rename-slice", Detail: "no relay.slice in policy.json", Skipped: true}, nil // name-guard: legacy
	}

	if dryRun {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("rename %s to relevo.slice in %s", legacy.Slice, path)}, nil
	}

	fi, err := os.Stat(path)
	if err != nil {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("stat %s: %v", path, err)}, err
	}
	out := normalizeLegacy(data)
	if err := writeFileAtomic(path, out, fi.Mode().Perm()); err != nil {
		return Step{Name: "rename-slice", Detail: fmt.Sprintf("write %s: %v", path, err)}, err
	}
	return Step{Name: "rename-slice", Detail: fmt.Sprintf("renamed slice in %s", path)}, nil
}

// writeFileAtomic replaces path with data via a temp file so a crash cannot truncate it.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
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
