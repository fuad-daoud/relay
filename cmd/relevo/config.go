package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// configUsage is the whole `relevo config` surface: what a bare `relevo config`
// does, and every subcommand that replaced a top-level verb (§4.1).
const configUsage = `usage: relevo config [--probe [token...]]
       relevo config export
       relevo config import <file|->
       relevo config get <section>[.<key>...]
       relevo config set <section>[.<key>...] <json>
       relevo config unset <section>[.<key>...]
       relevo config edit
       relevo config log [-n N] [--rev N] [--json]
       relevo config rollback <rev> [--yes] [-m <message>]
       relevo config init [--force] [--no-roles]
       relevo config agents [--kind <agy|claude|opencode>] [--role <name>] [--force] [--dry-run]
       relevo config server add <name> <url> [--fingerprint F] [--ca system] [--insecure]
       relevo config server rm <name>
       relevo config server list
       relevo config server key
       relevo config secret set <typesafe|client.key>
       relevo config secret rm <typesafe|client.key>
       relevo config secret list`

// cmdConfig is the one verb that replaced init, candidates, policy, roles,
// agent, client and servers (§4.1). A bare invocation, or one whose first
// argument is a flag, shows the configuration; every subcommand reuses the
// body the removed verb had.
func cmdConfig(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return configShow(args)
	}

	switch args[0] {
	case "export":
		return configExport(args[1:])
	case "import":
		return configImport(args[1:])
	case "get":
		return configGet(args[1:])
	case "set":
		return configSet(args[1:])
	case "unset":
		return configUnset(args[1:])
	case "edit":
		return configEdit(args[1:])
	case "log":
		return cmdConfigLog(args[1:])
	case "rollback":
		return cmdConfigRollback(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "roles-init":
		fmt.Fprintln(os.Stderr, "relevo config roles-init is gone: roles migrate to actors on their own (relevo config log)")
		return exitCodeErr{code: 2}
	case "agents":
		return cmdAgentInstall(args[1:])
	case "server":
		return configServer(args[1:])
	case "secret":
		return configSecret(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, configUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo config: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, configUsage)
		return exitCodeErr{code: 2}
	}
}

// configShow is the bare `relevo config`: the actors block, the current pick
// and the candidates block, each under a one-line heading. `--probe` runs
// exactly cmdCandidates --probe and prints nothing else.
func configShow(args []string) error {
	fs := flag.NewFlagSet("relevo config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	probe := fs.Bool("probe", false, "run each candidate once with a one-line prompt from this machine and record its time to first output")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if *probe {
		return cmdCandidates(args)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// The first block is the actors section after round 2's migration; a
	// config that still has no actors (a newer schema this binary did not
	// migrate) keeps today's roles block under its own heading (R7).
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	if len(L.Actors) > 0 {
		fmt.Println("actors")
		fmt.Print(relevo.FormatActors(L, rt.RoleRegistry()))
	} else {
		fmt.Println("roles")
		fmt.Print(relevo.FormatRoles(rt.RoleRegistry()))
	}
	fmt.Println("pick")
	fmt.Print(formatPolicy(rt))
	fmt.Println("candidates")
	fmt.Print(formatCandidates(rt))
	return nil
}

// configExport writes the whole-config document (§3) to stdout, indented, with
// one key per section present in config.Sections order.
func configExport(args []string) error {
	fs := flag.NewFlagSet("relevo config export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config export")
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	out, err := exportDoc(rt)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}

// configImport reads a document from a file (or stdin for "-"), stores it via
// PutDoc, and prints any warnings to stderr.
func configImport(args []string) error {
	fs := flag.NewFlagSet("relevo config import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config import <file|->")
		return exitCodeErr{code: 2}
	}

	var data []byte
	var err error
	if rest[0] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(rest[0])
	}
	if err != nil {
		return err
	}

	doc, err := decodeDoc(data)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	warnings, err := rt.Config.As("cli", "config import "+rest[0]).PutDoc(doc)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "relevo: "+w)
	}
	return nil
}

// configGet prints the JSON value at a path, indented. A missing section or
// key exits 1 with `relevo: <path>: not set`.
func configGet(args []string) error {
	fs := flag.NewFlagSet("relevo config get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config get <section>[.<key>...]")
		return exitCodeErr{code: 2}
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: not set", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	body, present, err := rt.Config.Body(sec)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%s: not set", path)
	}
	value, ok := jsonAt(body, keys)
	if !ok {
		return fmt.Errorf("%s: not set", path)
	}
	return printJSON(os.Stdout, value)
}

// configSet sets a JSON value at a path, creating intermediate objects. A
// value that is not valid JSON is stored as a JSON string. A path through a
// non-object exits 1. With no key the whole section is replaced.
func configSet(args []string) error {
	fs := flag.NewFlagSet("relevo config set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relevo config set <section>[.<key>...] <json>")
		return exitCodeErr{code: 2}
	}
	path, raw := rest[0], rest[1]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: unknown section", path)
	}
	value := rawJSONOrString(raw)

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		_, err := rt.Config.As("cli", "config set "+path).Put(sec, value)
		return err
	}

	body := []byte("{}")
	if stored, present, err := rt.Config.Body(sec); err != nil {
		return err
	} else if present {
		body = stored
	}
	updated, err := setJSON(body, keys, value)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	_, err = rt.Config.As("cli", "config set "+path).Put(sec, updated)
	return err
}

// configUnset removes a key, or, with no key, deletes the whole section. A
// path through a non-object, or a key that is not set, exits 1.
func configUnset(args []string) error {
	fs := flag.NewFlagSet("relevo config unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config unset <section>[.<key>...]")
		return exitCodeErr{code: 2}
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: unknown section", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return rt.Config.As("cli", "config unset "+path).Delete(sec)
	}

	body, present, err := rt.Config.Body(sec)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%s: not set", path)
	}
	updated, removed, err := unsetJSON(body, keys)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	if !removed {
		return fmt.Errorf("%s: not set", path)
	}
	_, err = rt.Config.As("cli", "config unset "+path).Put(sec, updated)
	return err
}

// configEdit opens the whole-config document in $VISUAL, else $EDITOR, else
// vi, and stores what the editor leaves (§4.2). A file left empty aborts, an
// unchanged file is a no-op, and an invalid document goes round again with the
// editor so the user can fix it.
func configEdit(args []string) error {
	fs := flag.NewFlagSet("relevo config edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config edit")
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	doc, err := exportDoc(rt)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = []byte("{}\n")
	}

	tmp, err := os.CreateTemp("", "relevo-config-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	for {
		if err := os.WriteFile(tmpPath, doc, 0o600); err != nil {
			return err
		}

		code, err := runEditor(tmpPath)
		if err != nil {
			return err
		}
		if code != 0 {
			fmt.Fprintf(os.Stderr, "relevo config edit: editor exited %d; nothing changed\n", code)
			return exitCodeErr{code: 1}
		}

		edited, err := os.ReadFile(tmpPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(edited)) == "" {
			fmt.Println("aborted; nothing changed")
			return nil
		}
		if bytes.Equal(edited, doc) {
			fmt.Println("no changes")
			return nil
		}

		parsed, perr := decodeDoc(edited)
		if perr == nil {
			perr = validateDoc(parsed)
		}
		if perr != nil {
			fmt.Fprintf(os.Stderr, "relevo config edit: %v\n", perr)
			doc = edited
			continue
		}

		warnings, err := rt.Config.As("cli", "config edit").PutDoc(parsed)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "relevo: "+w)
		}
		v, err := rt.Config.Version()
		if err != nil {
			return err
		}
		var rev int64
		if rows, err := rt.Config.Log(1); err == nil && len(rows) > 0 {
			rev = rows[0].Rev
		}
		fmt.Printf("saved (config version %d, revision #%d)\n", v, rev)
		return nil
	}
}

// runEditor runs $VISUAL, else $EDITOR, else vi (split on spaces) with path
// appended, its standard streams attached. It returns the editor's exit code.
func runEditor(path string) (int, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	argv := append(strings.Fields(editor), path)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

// configServer is the server half of `relevo config`: this machine's
// remote-builder identity and its configured servers (§4.1).
func configServer(args []string) error {
	const usage = `usage: relevo config server add <name> <url> (--fingerprint sha256:<hex> | --ca system | --insecure)
       relevo config server rm <name>
       relevo config server list
       relevo config server key`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "add":
		return cmdClientAddServer(args[1:])
	case "rm":
		return cmdClientRmServer(args[1:])
	case "list":
		return cmdServers(args[1:])
	case "key":
		return configServerKey(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo config server: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// configServerKey prints the client id and the enrolment line, generating the
// key when it is absent. This is what `client init` printed.
func configServerKey(args []string) error {
	fs := flag.NewFlagSet("relevo config server key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	pem, _, err := ensureClientKey(rt)
	if err != nil {
		return err
	}
	return printClientKey(pem)
}

// configSecret is the secrets half of `relevo config` (§4.1). A list prints
// names only, never values.
func configSecret(args []string) error {
	const usage = `usage: relevo config secret set <typesafe|client.key>
       relevo config secret rm <typesafe|client.key>
       relevo config secret list`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "set":
		return configSecretSet(args[1:])
	case "rm":
		return configSecretRm(args[1:])
	case "list":
		return configSecretList(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo config secret: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// checkSecretName refuses a name other than the two secrets relevo stores,
// exiting 2 with the allowed names.
func checkSecretName(name string) error {
	if name != config.SecretTypesafe && name != config.SecretClientKey {
		fmt.Fprintf(os.Stderr, "relevo config secret: unknown secret %q (allowed: %s, %s)\n",
			name, config.SecretTypesafe, config.SecretClientKey)
		return exitCodeErr{code: 2}
	}
	return nil
}

// configSecretSet reads the value from stdin, trimmed, and stores it. The
// client key is validated by PutSecret.
func configSecretSet(args []string) error {
	fs := flag.NewFlagSet("relevo config secret set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config secret set <typesafe|client.key>")
		return exitCodeErr{code: 2}
	}
	name := rest[0]
	if err := checkSecretName(name); err != nil {
		return err
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	value := strings.TrimSpace(string(data))

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := rt.Config.As("cli", "config secret set "+name).PutSecret(name, []byte(value)); err != nil {
		return err
	}
	fmt.Printf("stored secret %s\n", name)
	return nil
}

// configSecretRm removes a stored secret.
func configSecretRm(args []string) error {
	fs := flag.NewFlagSet("relevo config secret rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config secret rm <typesafe|client.key>")
		return exitCodeErr{code: 2}
	}
	name := rest[0]
	if err := checkSecretName(name); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if err := rt.Config.As("cli", "config secret rm "+name).SecretDelete(name); err != nil {
		return err
	}
	fmt.Printf("removed secret %s\n", name)
	return nil
}

// configSecretList prints the stored secret names, never their values.
func configSecretList(args []string) error {
	fs := flag.NewFlagSet("relevo config secret list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	names, err := rt.Config.SecretNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}

// exportDoc renders every stored section as the whole-config document (§3).
func exportDoc(rt relevo.Runtime) ([]byte, error) {
	doc := make(map[config.Section]json.RawMessage)
	for _, sec := range config.Sections {
		body, present, err := rt.Config.Body(sec)
		if err != nil {
			return nil, err
		}
		if present {
			doc[sec] = body
		}
	}
	return config.EncodeDoc(doc)
}

// decodeDoc parses a whole-config document into sections. The top level must
// be a JSON object.
func decodeDoc(data []byte) (map[config.Section]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("config document is not a JSON object")
	}
	doc := make(map[config.Section]json.RawMessage, len(raw))
	for name, body := range raw {
		doc[config.Section(name)] = body
	}
	return doc, nil
}

// validateDoc validates every section in doc, and refuses a section name
// relevo does not know.
func validateDoc(doc map[config.Section]json.RawMessage) error {
	for sec := range doc {
		if !configSectionKnown(sec) {
			return fmt.Errorf("unknown config section %q", sec)
		}
	}
	for _, sec := range config.Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		if _, err := config.Validate(sec, body); err != nil {
			return err
		}
	}
	return nil
}

// configPath splits <section>[.<key>...] into its section and key path. ok is
// false for an empty path or a section name relevo does not know.
func configPath(path string) (config.Section, []string, bool) {
	if path == "" {
		return "", nil, false
	}
	parts := strings.Split(path, ".")
	sec := config.Section(parts[0])
	if !configSectionKnown(sec) {
		return "", nil, false
	}
	return sec, parts[1:], true
}

// configSectionKnown reports whether sec names a stored section.
func configSectionKnown(sec config.Section) bool {
	for _, s := range config.Sections {
		if s == sec {
			return true
		}
	}
	return false
}

// jsonAt walks keys into a JSON body, returning the raw value there. A key
// missing, or a body that is not an object at some step, is ok false.
func jsonAt(body []byte, keys []string) (json.RawMessage, bool) {
	cur := json.RawMessage(body)
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// setJSON returns body with value stored at keys, creating intermediate
// objects. A path that runs through a non-object is an error.
func setJSON(body []byte, keys []string, value json.RawMessage) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("not an object")
	}
	if root == nil {
		return nil, errors.New("not an object")
	}
	if err := setAt(root, keys, value); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func setAt(obj map[string]json.RawMessage, keys []string, value json.RawMessage) error {
	key := keys[0]
	if len(keys) == 1 {
		obj[key] = value
		return nil
	}

	child := map[string]json.RawMessage{}
	if existing, ok := obj[key]; ok {
		if err := json.Unmarshal(existing, &child); err != nil || child == nil {
			return errors.New("not an object")
		}
	}
	if err := setAt(child, keys[1:], value); err != nil {
		return err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return err
	}
	obj[key] = encoded
	return nil
}

// unsetJSON returns body with the key at keys removed, and removed false when
// the path is missing. A path that runs through a non-object is an error.
func unsetJSON(body []byte, keys []string) (out []byte, removed bool, err error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, errors.New("not an object")
	}
	if root == nil {
		return nil, false, errors.New("not an object")
	}
	removed, err = delAt(root, keys)
	if err != nil {
		return nil, false, err
	}
	if !removed {
		return nil, false, nil
	}
	out, err = json.Marshal(root)
	return out, true, err
}

func delAt(obj map[string]json.RawMessage, keys []string) (bool, error) {
	key := keys[0]
	if len(keys) == 1 {
		if _, ok := obj[key]; !ok {
			return false, nil
		}
		delete(obj, key)
		return true, nil
	}

	existing, ok := obj[key]
	if !ok {
		return false, nil
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(existing, &child); err != nil || child == nil {
		return false, errors.New("not an object")
	}
	removed, err := delAt(child, keys[1:])
	if err != nil || !removed {
		return false, err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return false, err
	}
	obj[key] = encoded
	return true, nil
}

// rawJSONOrString is the value `config set` stores: raw is kept when it is
// valid JSON, and otherwise treated as a JSON string.
func rawJSONOrString(raw string) json.RawMessage {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	encoded, _ := json.Marshal(raw)
	return encoded
}

// printJSON writes raw as indented JSON on its own line.
func printJSON(w io.Writer, raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := w.Write(out.Bytes())
	return err
}

// ensureClientKey returns the stored client key, generating and storing one
// when it is absent. generated reports whether it had to create one.
func ensureClientKey(rt relevo.Runtime) (pem []byte, generated bool, err error) {
	if stored, ok, err := rt.Config.Secret(config.SecretClientKey); err != nil {
		return nil, false, err
	} else if ok {
		return stored, false, nil
	}

	kp, err := remote.Generate()
	if err != nil {
		return nil, false, err
	}
	pem, err = remote.MarshalPrivate(kp)
	if err != nil {
		return nil, false, err
	}
	if err := rt.Config.As("cli", "config server key").PutSecret(config.SecretClientKey, pem); err != nil {
		return nil, false, err
	}
	return pem, true, nil
}

// printClientKey prints the client id and the enrolment line a server admin
// runs `relevo serve enroll --key "<line>"` with.
func printClientKey(pem []byte) error {
	kp, err := remote.ParsePrivate(pem)
	if err != nil {
		return err
	}
	fmt.Printf("client id %s\n", remote.IDOf(kp.Public))
	fmt.Println(client.EnrollLine(kp))
	return nil
}

// stdinStat is the terminal test's only seam: production stats os.Stdin, and a
// test can replace it. It is the pattern internal/ui/source.go:115-118 and
// internal/pick/pick.go:27 use, with no new dependency.
var stdinStat = os.Stdin.Stat

// stdinIsTerminal reports whether os.Stdin is a character device.
func stdinIsTerminal() bool {
	info, err := stdinStat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// revisionJSON is the --json shape of one revision (§6.4). Snapshot is present
// only for `--rev`, where it is the raw snapshot object.
type revisionJSON struct {
	Rev      int64           `json:"rev"`
	At       string          `json:"at"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
	Version  int64           `json:"version"`
	Changes  json.RawMessage `json:"changes"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// revChangeList decodes a revision row's raw change list; a row whose changes
// do not parse reads as no changes rather than failing the whole listing.
func revChangeList(r db.RevisionRow) []config.Change {
	var cs []config.Change
	if err := json.Unmarshal(r.Changes, &cs); err != nil {
		return nil
	}
	return cs
}

func revisionJSONOf(r db.RevisionRow, withSnapshot bool) revisionJSON {
	changes := r.Changes
	if !json.Valid(changes) {
		changes = json.RawMessage("[]")
	}
	out := revisionJSON{
		Rev:     r.Rev,
		At:      r.At.UTC().Format(time.RFC3339),
		Source:  r.Source,
		Message: r.Message,
		Version: r.Version,
		Changes: changes,
	}
	if withSnapshot {
		snapshot := r.Snapshot
		if !json.Valid(snapshot) {
			snapshot = json.RawMessage("{}")
		}
		out.Snapshot = snapshot
	}
	return out
}

// printRevisionText prints one revision's header, its message when it has one,
// and one Describe line per change indented by two spaces.
func printRevisionText(r db.RevisionRow) error {
	fmt.Printf("#%d  %s  %s  config version %d\n",
		r.Rev, r.At.Local().Format("2006-01-02 15:04"), r.Source, r.Version)
	if r.Message != "" {
		fmt.Printf("message: %s\n", r.Message)
	}
	if r.Source == "baseline" {
		fmt.Println("  (baseline: the config as it was before revisions were recorded)")
		return nil
	}
	for _, c := range revChangeList(r) {
		fmt.Printf("  %s\n", config.Describe(c))
	}
	return nil
}

// cmdConfigLog lists revisions newest first, or shows one with --rev. The list
// is a header line per revision; --rev adds the message and every change.
func cmdConfigLog(args []string) error {
	fs := flag.NewFlagSet("relevo config log", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	n := fs.Int("n", 20, "how many revisions to list")
	rev := fs.Int64("rev", 0, "show one revision: its header and changes")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config log [-n N] [--rev N] [--json]")
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *rev > 0 {
		r, err := rt.Config.Revision(*rev)
		if err != nil {
			if errors.Is(err, config.ErrNoRevision) {
				return fmt.Errorf("no such revision #%d", *rev)
			}
			return err
		}
		if *asJSON {
			out, err := json.Marshal(revisionJSONOf(r, true))
			if err != nil {
				return err
			}
			return printJSON(os.Stdout, out)
		}
		return printRevisionText(r)
	}

	rows, err := rt.Config.Log(*n)
	if err != nil {
		return err
	}
	if *asJSON {
		list := make([]revisionJSON, 0, len(rows))
		for _, r := range rows {
			list = append(list, revisionJSONOf(r, false))
		}
		out, err := json.Marshal(list)
		if err != nil {
			return err
		}
		return printJSON(os.Stdout, out)
	}
	if len(rows) == 0 {
		fmt.Println("no revisions yet")
		return nil
	}
	for _, r := range rows {
		fmt.Printf("#%d  %s  %-9s  %d change(s)  %s\n",
			r.Rev, r.At.Local().Format("2006-01-02 15:04"), r.Source, len(revChangeList(r)), r.Message)
	}
	return nil
}

// cmdConfigRollback prints what rolling back to rev would change, confirms
// with the user unless --yes, then writes the rollback as one new revision.
func cmdConfigRollback(args []string) error {
	fs := flag.NewFlagSet("relevo config rollback", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	msg := fs.String("m", "", "message for the rollback revision")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config rollback <rev> [--yes] [-m <message>]")
		return exitCodeErr{code: 2}
	}
	rev, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || rev <= 0 {
		fmt.Fprintf(os.Stderr, "relevo config rollback: not a revision number: %q\n", rest[0])
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	plan, err := rt.Config.RollbackPlan(rev)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrNoRevision):
			return fmt.Errorf("no such revision #%d", rev)
		case errors.Is(err, config.ErrNoChange):
			fmt.Printf("config already equals revision #%d\n", rev)
			return nil
		default:
			return err
		}
	}

	fmt.Printf("rollback to #%d would change:\n", rev)
	for _, c := range plan {
		fmt.Println(config.Describe(c))
	}

	if !*yes {
		if !stdinIsTerminal() {
			fmt.Fprintln(os.Stderr, "relevo: config rollback needs a terminal to confirm; pass --yes")
			return exitCodeErr{code: 2}
		}
		fmt.Printf("Roll back to #%d? [y/N] ", rev)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Println("nothing changed")
			return exitCodeErr{code: 1}
		}
	}

	row, err := rt.Config.As("rollback", *msg).Rollback(rev)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrNoRevision):
			return fmt.Errorf("no such revision #%d", rev)
		case errors.Is(err, config.ErrNoChange):
			fmt.Printf("config already equals revision #%d\n", rev)
			return nil
		default:
			return err
		}
	}
	fmt.Printf("rolled back to #%d as #%d (config version %d)\n", rev, row.Rev, row.Version)
	return nil
}
