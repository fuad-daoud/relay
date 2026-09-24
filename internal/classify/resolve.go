package classify

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// Resolve builds the runtime's classifier from the policy block. nil block ->
// (nil, Status{Configured:false}). Key lookup: getenv("TYPESAFE_API_KEY")
// trimmed; else key, the TypeSafe API key stored in the database, trimmed. No
// key -> (Unavailable{Reason}, Status{Configured:true, KeySource:""}).
// Otherwise a *Client with the block's model. Never returns an error: a bad
// key is found by the first request (401), and a missing one is a doctor row,
// not a crash.
func Resolve(cfg *policy.Classify, key string, getenv func(string) string) (Classifier, Status) {
	if cfg == nil {
		return nil, Status{Configured: false}
	}

	st := Status{
		Configured: true,
		Provider:   cfg.Provider,
		Model:      cfg.ModelName(),
	}

	if getenv != nil {
		if envKey := strings.TrimSpace(getenv("TYPESAFE_API_KEY")); envKey != "" {
			st.KeySource = "env"
			return NewClient(envKey, cfg.ModelName()), st
		}
	}

	if dbKey := strings.TrimSpace(key); dbKey != "" {
		st.KeySource = "db"
		return NewClient(dbKey, cfg.ModelName()), st
	}

	st.KeySource = ""
	return Unavailable{Reason: "no classifier key"}, st
}
