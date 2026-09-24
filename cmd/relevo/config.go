package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
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
       relevo config init [--force] [--no-roles]
       relevo config roles-init [--force] [--dry-run]
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
	case "init":
		return cmdInit(args[1:])
	case "roles-init":
		return cmdRolesInit(args[1:])
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

// configShow is the bare `relevo config`: the roles block, the pick block and
// the candidates block, each under a one-line heading. `--probe` runs exactly
// cmdCandidates --probe and prints nothing else.
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

	fmt.Println("roles")
	fmt.Print(relevo.FormatRoles(rt.RoleRegistry()))
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
	warnings, err := rt.Config.PutDoc(doc)
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
		_, err := rt.Config.Put(sec, value)
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
	_, err = rt.Config.Put(sec, updated)
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
		return rt.Config.Delete(sec)
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
	_, err = rt.Config.Put(sec, updated)
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

		warnings, err := rt.Config.PutDoc(parsed)
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
		fmt.Printf("saved (config version %d)\n", v)
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
	if err := rt.Config.PutSecret(name, []byte(value)); err != nil {
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
	if err := rt.Config.SecretDelete(name); err != nil {
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
	return encodeDoc(doc)
}

// encodeDoc renders doc as an indented JSON object with one key per section,
// in config.Sections order. A map's own keys would be sorted alphabetically,
// so the object is assembled by hand and then indented.
func encodeDoc(doc map[config.Section]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, sec := range config.Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, body); err != nil {
			return nil, fmt.Errorf("%s: %v", sec, err)
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		key, err := json.Marshal(string(sec))
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(compact.Bytes())
	}
	buf.WriteByte('}')

	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
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
	if err := rt.Config.PutSecret(config.SecretClientKey, pem); err != nil {
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
