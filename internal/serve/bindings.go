package serve

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

// Allowed reports whether caller is authorized to perform verb on binding b (spec §2.5).
// Exported for testing and for future grants lookup (#203).
func Allowed(caller remote.ClientID, verb string, b store.Binding) bool {
	if verb == "create" {
		return true
	}
	return b.Owner == string(caller)
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func (s *Server) loadBinding(caller remote.ClientID, name string) (store.Binding, relay.Runtime, error) {
	if caller != "" {
		rt := s.runtime(caller)
		if b, err := rt.Store.Load(name); err == nil {
			return b, rt, nil
		}
	}
	for _, cl := range s.clients.List() {
		if cl.ID == caller {
			continue
		}
		rt := s.runtime(cl.ID)
		if b, err := rt.Store.Load(name); err == nil {
			return b, rt, nil
		}
	}
	return store.Binding{}, relay.Runtime{}, store.ErrNotFound
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var req remote.CreateBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	if err := store.ValidName(req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	if req.RepoID == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "repo_id is required")
		return
	}
	if len(req.BaseCommit) != 40 || !isHex(req.BaseCommit) {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "base_commit must be 40 hex characters")
		return
	}

	caller := callerOf(r)
	rt := s.runtime(caller)

	if _, err := rt.Store.Load(req.Name); err == nil {
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "binding exists")
		return
	}

	bare := filepath.Join(s.cfg.Root, "repos", string(caller), req.RepoID+".git")
	if err := s.cfg.Git.InitBare(r.Context(), bare); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	now := s.cfg.Now()
	candidateToken, harnessKind := relay.PickServedCandidate(rt, req.Candidate)

	cwd := rt.Store.WorktreePath(req.Name)
	b := store.Binding{
		Name:             req.Name,
		Owner:            string(caller),
		CWD:              cwd,
		Worktree:         cwd,
		Branch:           "relay/" + req.Name,
		Base:             req.BaseCommit,
		Repo:             bare,
		Builder:          store.Endpoint{Kind: harnessKind, Mode: store.ModeHeadless, AgentName: req.Name},
		BuilderCandidate: candidateToken,
		Round:            1,
		State:            store.StateActive,
		RoundCap:         req.RoundCap,
		RoundTimeoutMS:   req.RoundTimeoutMS,
		Serve: &store.ServeFacts{
			RepoID:   req.RepoID,
			BareRepo: bare,
			LastSeen: now,
		},
	}

	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(req.Name); err == nil {
		b = reloaded
	}

	entries, _ := rt.Store.ReadLog(req.Name)
	writeJSON(w, http.StatusCreated, relay.ServedView(b, entries))
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt := s.runtime(caller)

	bindings, err := rt.Store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	views := make([]remote.BindingView, 0, len(bindings))
	for _, b := range bindings {
		if !Allowed(caller, "list", b) {
			continue
		}
		entries, _ := rt.Store.ReadLog(b.Name)
		views = append(views, relay.ServedView(b, entries))
	}

	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil || !Allowed(caller, "get", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	now := s.cfg.Now()
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.LastSeen = now
	_ = rt.Store.Save(b)

	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relay.ServedView(b, entries))
}

func (s *Server) handleDone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil || !Allowed(caller, "done", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	if relay.RoundStateOf(b, entries) == remote.RoundRunning {
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, "round is open")
		return
	}

	_, err = relay.Done(r.Context(), rt, name)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "round") && strings.Contains(errStr, "open") {
			writeErr(w, http.StatusConflict, remote.CodeRoundOpen, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relay.ServedView(b, entries))
}

func (s *Server) handleUnbind(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil || !Allowed(caller, "unbind", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	_, err = relay.Unbind(r.Context(), rt, name, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil || !Allowed(caller, "resume", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	if b.State != store.StateDone {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "binding is not done")
		return
	}
	if b.Serve == nil || b.Serve.BareRepo == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "missing bare repo facts")
		return
	}

	if err := s.cfg.Git.CheckoutWorktree(r.Context(), b.Serve.BareRepo, b.Worktree, b.Branch); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relay.ServedView(b, entries))
}

func (s *Server) handleUnavailable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt := s.runtime(caller)

	name := r.PathValue("name")
	if name != "" {
		b, _, err := s.loadBinding(caller, name)
		if err != nil || !Allowed(caller, "unavailable", b) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
	}

	var req remote.UnavailableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "token is required")
		return
	}

	if _, err := relay.Unavailable(rt, req.Token, time.Time{}, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}
