package classify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relay/internal/policy"
)

// Resolve builds the runtime's classifier from the policy block. nil block ->
// (nil, Status{Configured:false}). Key lookup: getenv("TYPESAFE_API_KEY")
// trimmed; else the trimmed contents of filepath.Join(configDir, "relay",
// "typesafe.key"); a readable but empty file counts as no key. No key ->
// (Unavailable{Reason}, Status{Configured:true, KeySource:""}). Otherwise a
// *Client with the block's model. Never returns an error: a bad key is found
// by the first request (401), and a missing one is a doctor row, not a crash.
func Resolve(cfg *policy.Classify, configDir string, getenv func(string) string) (Classifier, Status) {
	if cfg == nil {
		return nil, Status{Configured: false}
	}

	keyPath := filepath.Join(configDir, "relay", "typesafe.key")
	st := Status{
		Configured: true,
		Provider:   cfg.Provider,
		Model:      cfg.ModelName(),
		KeyPath:    keyPath,
	}

	if getenv != nil {
		if envKey := strings.TrimSpace(getenv("TYPESAFE_API_KEY")); envKey != "" {
			st.KeySource = "env"
			return NewClient(envKey, cfg.ModelName()), st
		}
	}

	if data, err := os.ReadFile(keyPath); err == nil {
		if fileKey := strings.TrimSpace(string(data)); fileKey != "" {
			st.KeySource = "file"
			return NewClient(fileKey, cfg.ModelName()), st
		}
	}

	st.KeySource = ""
	reason := fmt.Sprintf("no key: set TYPESAFE_API_KEY or write %s", keyPath)
	return Unavailable{Reason: reason}, st
}
