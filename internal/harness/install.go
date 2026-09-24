package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// InstallEnv defines the filesystem and PATH seams required by Install.
type InstallEnv interface {
	// LookPath looks up binary on PATH. Any error means not on PATH.
	LookPath(binary string) (string, error)
	// HomePath joins rel under the user's home directory.
	// An error aborts the install because nothing can be written.
	HomePath(rel string) (string, error)
	// ReadFile reads the named file. When absent, it returns an error satisfying errors.Is(err, fs.ErrNotExist).
	// Any other error is treated as "present, differs".
	ReadFile(path string) ([]byte, error)
	// MkdirAll creates dir and parents, with permission 0755.
	MkdirAll(dir string) error
	// WriteFile writes whole file with permission 0644, truncating if it exists.
	WriteFile(path string, data []byte) error
	// LoadManifest reads the role manifest (#371 §4.10): a home-relative
	// path (Role.Path) to the lowercase hex sha256 of what relevo last wrote
	// there. A missing manifest is an empty map, never an error.
	LoadManifest() (map[string]string, error)
	// SaveManifest writes the manifest. Install calls it once per Install,
	// and only when the map changed and opts.DryRun is false.
	SaveManifest(map[string]string) error
}

// InstallOptions controls the behavior of Install.
type InstallOptions struct {
	Kind   string
	Role   string
	Force  bool
	DryRun bool
	// Files opts a kind's shipped files in (#393 §5.4): when true, an
	// absent shipped file is written; when false, an absent shipped file
	// produces no result row (the OpenCode plugin is opt-in). A file
	// present on disk follows the definition rules either way.
	Files bool
}

// InstallOutcome represents the outcome of an install action for a single role.
type InstallOutcome string

const (
	OutcomeWrote          InstallOutcome = "wrote"
	OutcomeOverwrote      InstallOutcome = "overwrote"
	OutcomeUpdated        InstallOutcome = "updated (unchanged since relevo wrote it)"
	OutcomeKeptIdentical  InstallOutcome = "kept (identical)"
	OutcomeKeptDiffers    InstallOutcome = "kept (differs; --force to overwrite)"
	OutcomeWouldWrite     InstallOutcome = "would write"
	OutcomeWouldOverwrite InstallOutcome = "would overwrite"
	OutcomeWouldUpdate    InstallOutcome = "would update"
	OutcomeError          InstallOutcome = "error"
)

// InstallResult describes the outcome of installing one role definition.
type InstallResult struct {
	Kind    string
	Role    string
	Path    string
	Outcome InstallOutcome
	Err     string
}

// ErrUnknownKind is returned when the requested harness kind is unknown.
var ErrUnknownKind = errors.New("unknown harness kind")

// ErrUnknownRole is returned when the requested role is not shipped by any candidate harness.
var ErrUnknownRole = errors.New("unknown role")

// DocEqual reports whether shipped and installed definitions are equal after trimming trailing whitespace (" \t\r\n").
func DocEqual(shipped, installed []byte) bool {
	return bytes.Equal(bytes.TrimRight(shipped, " \t\r\n"), bytes.TrimRight(installed, " \t\r\n"))
}

// Install decides, per (kind, role), whether the shipped definition lands on disk, and lands it.
// Results are ordered by harness.All() (sorted by kind), and roles by Harness.Roles table order.
// Per-file failures are reported as error outcomes, never as returned errors.
//
// The role manifest (#371 §4.10) is loaded once, threaded through every
// installOne decision, and saved once at the end -- only if it changed, and
// never under DryRun. A manifest that cannot be read is reported once as the
// returned error (with the results still returned) and treated as empty, so a
// corrupt file never blocks a definition from landing.
func Install(env InstallEnv, opts InstallOptions) ([]InstallResult, error) {
	manifest, merr := env.LoadManifest()
	if merr != nil || manifest == nil {
		manifest = map[string]string{}
	}

	var kinds []Harness
	if opts.Kind != "" {
		h, ok := Lookup(opts.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, opts.Kind)
		}
		kinds = []Harness{h}
	} else {
		for _, h := range All() {
			if _, err := env.LookPath(h.Binary); err == nil {
				kinds = append(kinds, h)
			}
		}
	}

	if opts.Role != "" {
		candidates := kinds
		if len(candidates) == 0 {
			candidates = All()
		}
		found := false
		for _, c := range candidates {
			if _, ok := c.Role(opts.Role); ok {
				found = true
				break
			}
		}
		if !found {
			knownMap := make(map[string]struct{})
			for _, c := range candidates {
				for _, name := range c.RoleNames() {
					knownMap[name] = struct{}{}
				}
			}
			known := make([]string, 0, len(knownMap))
			for name := range knownMap {
				known = append(known, name)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownRole, opts.Role, known)
		}
	}

	var results []InstallResult
	changed := false
	for _, h := range kinds {
		for _, r := range h.Roles {
			if opts.Role != "" && r.Name != opts.Role {
				continue
			}
			res, rchanged, err := installOne(env, opts, h.Kind, r, manifest)
			if err != nil {
				return nil, err
			}
			changed = changed || rchanged
			results = append(results, res)
		}

		// Shipped files (#393 §5.4): the OpenCode plugin package, opt-in.
		// An absent file is written only when opts.Files is set; otherwise
		// it produces no row. A file present on disk follows the definition
		// rules in both cases. Only a full install (no --role) touches
		// them, never a single-role probe like doctor's dry run.
		if opts.Role == "" {
			for _, f := range h.Files {
				shipped, err := ShippedFileBytes(h.Kind, f.Name)
				if err != nil {
					results = append(results, InstallResult{
						Kind:    h.Kind,
						Role:    f.Name,
						Path:    f.Path,
						Outcome: OutcomeError,
						Err:     err.Error(),
					})
					continue
				}
				full, err := env.HomePath(f.Path)
				if err != nil {
					return nil, err
				}
				if _, rerr := env.ReadFile(full); errors.Is(rerr, fs.ErrNotExist) && !opts.Files {
					continue
				}
				res, fchanged, err := installBytes(env, opts, InstallResult{Kind: h.Kind, Role: f.Name, Path: f.Path}, f.Path, shipped, manifest, "")
				if err != nil {
					return nil, err
				}
				changed = changed || fchanged
				results = append(results, res)
			}
		}
	}

	if changed && !opts.DryRun {
		if serr := env.SaveManifest(manifest); serr != nil {
			// The definitions landed; only the record of them did not. The
			// caller reports this once and keeps the results.
			return results, serr
		}
	}
	return results, merr
}

// agentDocBase is the embedded definition's basename for role r of kind:
// "<Role.Doc>.<DocExt or md>", exactly as AgentDoc composes its embed path
// (#371 round 3 §4). It is the key ShippedBefore indexes.
func agentDocBase(r Role, kind string) string {
	ext := "md"
	if h, ok := Lookup(kind); ok && h.DocExt != "" {
		ext = h.DocExt
	}
	return r.Doc + "." + ext
}

// installOne applies §5's decision table to one (kind, role). Its bool reports
// whether the manifest changed, so Install can save it once; a write that
// failed records nothing.
func installOne(env InstallEnv, opts InstallOptions, kind string, r Role, manifest map[string]string) (InstallResult, bool, error) {
	shipped, err := AgentDoc(r.Name, kind)
	if err != nil {
		return InstallResult{
			Kind:    kind,
			Role:    r.Name,
			Path:    r.Path,
			Outcome: OutcomeError,
			Err:     err.Error(),
		}, false, nil
	}
	return installBytes(env, opts, InstallResult{Kind: kind, Role: r.Name, Path: r.Path}, r.Path, shipped, manifest, agentDocBase(r, kind))
}

// installBytes applies §5's decision table to one shipped file: an agent
// definition (from installOne) or an opt-in shipped file (#393 §5.4). res names
// the target -- Kind, Role (the install label) and Path; homeRel is the
// home-relative path whose sha the manifest records (the same as res.Path);
// shipped is the bytes to land; shippedBeforeKey is the embedded basename (or
// file name) ShippedBefore indexes, "" for a shipped file, which has no
// pre-manifest history and so is consulted only when the key is non-empty. Its
// bool reports whether the manifest changed, so Install can save it once; a
// write that failed records nothing.
//
// The behaviour for a role is byte-for-byte the table installOne applied
// before the extraction: only the manifest key and the ShippedBefore key are
// parameters now.
func installBytes(env InstallEnv, opts InstallOptions, res InstallResult, homeRel string, shipped []byte, manifest map[string]string, shippedBeforeKey string) (InstallResult, bool, error) {
	full, err := env.HomePath(homeRel)
	if err != nil {
		return InstallResult{}, false, err
	}
	existing, rerr := env.ReadFile(full)
	existingSHA := docSHA(existing)
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		if opts.DryRun {
			res.Outcome = OutcomeWouldWrite
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeWrote)
		if res.Outcome != OutcomeWrote {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	case rerr == nil && DocEqual(shipped, existing):
		res.Outcome = OutcomeKeptIdentical
		return res, record(manifest, homeRel, existingSHA), nil
	case rerr == nil && (manifest[homeRel] == existingSHA || (shippedBeforeKey != "" && ShippedBefore(shippedBeforeKey, existingSHA))):
		// The bytes on disk are exactly what relevo last wrote, or a blob
		// some past relevo shipped before manifests existed, so the
		// difference from the shipped copy is relevo's own older release,
		// not the user's edit: safe to refresh (#371 §4.10, round 3 §4).
		if opts.DryRun {
			res.Outcome = OutcomeWouldUpdate
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeUpdated)
		if res.Outcome != OutcomeUpdated {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	default:
		if !opts.Force {
			res.Outcome = OutcomeKeptDiffers
			return res, false, nil
		}
		if opts.DryRun {
			res.Outcome = OutcomeWouldOverwrite
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeOverwrote)
		if res.Outcome != OutcomeOverwrote {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	}
}

// record stores path's sha in the manifest and reports whether that changed the
// map, so Install saves the file only when it actually did.
func record(manifest map[string]string, path, sha string) bool {
	if manifest[path] == sha {
		return false
	}
	manifest[path] = sha
	return true
}

func writeDoc(env InstallEnv, full string, data []byte, res InstallResult, success InstallOutcome) InstallResult {
	if err := env.MkdirAll(filepath.Dir(full)); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	if err := env.WriteFile(full, data); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	res.Outcome = success
	return res
}

// Line renders one output line for the result:
// "<outcome>  ~/<path>" for every outcome but error, and
// "error  ~/<path>: <err>" for that one.
func (r InstallResult) Line() string {
	if r.Outcome == OutcomeError {
		return fmt.Sprintf("error  ~/%s: %s", r.Path, r.Err)
	}
	return fmt.Sprintf("%s  ~/%s", r.Outcome, r.Path)
}

type osInstallEnv struct {
	// kv is the database the role manifest lives in, under the kv key
	// "agents-manifest" (P3b plan §4.4). Only OSInstallEnvKV sets it; a caller
	// that has no database gets an env that reads as "nothing recorded" and
	// refuses to save, rather than writing a manifest somewhere unintended
	// (#371 §3).
	kv db.KV
	// manifestPath is the legacy file the first read imports: <state
	// root>/agents-manifest.json. "" skips the import.
	manifestPath string
}

// OSInstallEnv returns an InstallEnv backed by the OS and exec packages,
// without a role manifest: it is for the seams that only resolve paths and
// read files (OSRoleChecker). Callers that install definitions and want the
// manifest maintained use OSInstallEnvKV.
func OSInstallEnv() InstallEnv {
	return osInstallEnv{}
}

// OSInstallEnvKV is OSInstallEnv with the role manifest kept in kv, importing a
// present legacy file at legacyPath (agents-manifest.json) on first read
// (#371 §4.10; P3b plan §4.4). It replaces OSInstallEnvAt: the caller opens the
// machine database -- which this package cannot, since harness <- usage <- store
// -- and passes the handle and the old path in.
func OSInstallEnvKV(kv db.KV, legacyPath string) InstallEnv {
	return osInstallEnv{kv: kv, manifestPath: legacyPath}
}

func (osInstallEnv) LookPath(binary string) (string, error) {
	return exec.LookPath(binary)
}

func (osInstallEnv) HomePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

func (osInstallEnv) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osInstallEnv) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// WriteFile writes through a temp file in the same directory, then renames, so
// a reader never sees a half-written definition (#371 §4.10).
func (osInstallEnv) WriteFile(path string, data []byte) error {
	return writeFileAtomic(path, data, 0o644)
}

func (e osInstallEnv) LoadManifest() (map[string]string, error) {
	if e.kv == nil {
		return map[string]string{}, nil
	}
	return ReadManifest(e.kv, e.manifestPath)
}

func (e osInstallEnv) SaveManifest(m map[string]string) error {
	if e.kv == nil {
		return errors.New("no role manifest store configured")
	}
	return WriteManifest(e.kv, m)
}
