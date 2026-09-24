package harness

import "embed"

// pluginFS is the embed of the OpenCode plugin package
// (internal/harness/opencodeplugin/): the files relevo ships beside its agent
// definitions and installs for kind opencode (#393 §5.4).
//
//go:embed opencodeplugin/package.json opencodeplugin/server.ts opencodeplugin/tui.tsx
var pluginFS embed.FS

// ShippedFile is one file relevo ships for a harness kind beside its agent
// definitions, installed under the user's home (#393 §5.4).
type ShippedFile struct {
	Name  string // install label and result "role" column: "opencode-plugin/package.json" etc.
	Path  string // home-relative install path: ".config/opencode/plugins/relevo/package.json"
	Embed string // path inside the embed FS: "opencodeplugin/package.json"
}

// ShippedFileBytes returns the embedded bytes of one shipped file of kind.
//
// The path read is the TABLE's Embed field, never the caller's name, so a
// caller cannot steer the read with path syntax (as AgentDoc for a role). An
// unknown kind or name returns ErrNoAgentDoc without touching the embed FS.
func ShippedFileBytes(kind, name string) ([]byte, error) {
	h, ok := Lookup(kind)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	for _, f := range h.Files {
		if f.Name != name {
			continue
		}
		b, err := pluginFS.ReadFile(f.Embed)
		if err != nil {
			return nil, ErrNoAgentDoc
		}
		return b, nil
	}
	return nil, ErrNoAgentDoc
}
