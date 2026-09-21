package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
)

type responseLogger struct {
	http.ResponseWriter
	status int
}

func (r *responseLogger) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code remote.Code, msg string) {
	writeJSON(w, status, remote.ErrorBody{
		Code:    code,
		Message: msg,
	})
}

// ownerLabel is the request log's owner field: the caller's enrolled label,
// falling back to its id prefix (Clients.LabelOf), or "-" when the request
// never authenticated at all -- caller is the zero ClientID on a signature
// failure, since the auth middleware never reaches the point of setting one.
// Without this, that case printed owner="" instead of naming the failure as
// what it is: nobody.
func ownerLabel(clients *Clients, caller remote.ClientID) string {
	if caller == "" {
		return "-"
	}
	return clients.LabelOf(caller)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	mux.HandleFunc("GET /v1/candidates", s.handleCandidates)
	mux.HandleFunc("POST /v1/bindings", s.handleCreateBinding)
	mux.HandleFunc("GET /v1/bindings", s.handleListBindings)
	mux.HandleFunc("GET /v1/bindings/{name}", s.handleGetBinding)
	mux.HandleFunc("POST /v1/bindings/{name}/done", s.handleDone)
	mux.HandleFunc("POST /v1/bindings/{name}/unbind", s.handleUnbind)
	mux.HandleFunc("POST /v1/bindings/{name}/resume", s.handleResume)
	mux.HandleFunc("POST /v1/bindings/{name}/rounds", s.handleStartRound)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/files/{kind}", s.handleRoundFile)
	mux.HandleFunc("GET /v1/bindings/{name}/rounds/{n}/bundle", s.handleRoundBundle)
	mux.HandleFunc("POST /v1/bindings/{name}/rounds/{n}/ack", s.handleAckRound)
	mux.HandleFunc("POST /v1/bindings/{name}/unavailable", s.handleUnavailable)
	mux.HandleFunc("POST /v1/unavailable", s.handleUnavailable)
	mux.HandleFunc("POST /v1/available", s.handleAvailable)

	// Fallback for unknown /v1/...
	mux.HandleFunc("/v1/", s.handleNotFound)

	authenticatedMux := s.authenticate(mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
			writeErr(w, http.StatusUpgradeRequired, remote.CodeVersion, "this server speaks v1")
			return
		}

		rw := &responseLogger{ResponseWriter: w, status: http.StatusOK}
		authenticatedMux.ServeHTTP(rw, r)

		owner := ownerLabel(s.clients, callerOf(r))
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "owner", owner, "status", rw.status)
	})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	label := s.clients.LabelOf(caller)
	who := remote.WhoAmI{
		ID:            caller,
		Label:         label,
		ServerVersion: remote.Version,
		Transports:    []string{"git-bundle"},
	}
	if rt, err := s.runtime(caller); err == nil {
		who.Features = []string{remote.FeatureTier}
		who.BuilderTier = string(relay.ServedBuilderTier(rt))
		who.MaxTier = string(rt.Policy.MaxTierOrDefault())
	}
	writeJSON(w, http.StatusOK, who)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
}
