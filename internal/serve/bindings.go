package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
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

func (s *Server) loadBinding(caller remote.ClientID, name string) (store.Binding, relevo.Runtime, error) {
	if caller == "" {
		return store.Binding{}, relevo.Runtime{}, store.ErrNotFound
	}
	rt, err := s.runtime(caller)
	if err != nil {
		return store.Binding{}, relevo.Runtime{}, err
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return store.Binding{}, relevo.Runtime{}, err
	}
	return b, rt, nil
}

// validAuthor reports whether a wire author is storable (#335): a non-empty
// name and email of at most 256 bytes each, with no newline, carriage
// return, NUL, < or >. The server puts both values in a builder's
// environment, so anything that could forge a line there is refused up
// front.
func validAuthor(a remote.GitIdentity) bool {
	return validAuthorPart(a.Name) && validAuthorPart(a.Email)
}

func validAuthorPart(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	return !strings.ContainsAny(s, "\n\r\x00<>")
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
	if req.Author != nil && !validAuthor(*req.Author) {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid,
			"author: name and email must be 1-256 bytes with no newline, NUL, < or >")
		return
	}

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	if _, err := rt.Store.Load(req.Name); err == nil {
		writeErr(w, http.StatusConflict, remote.CodeInvalid, "binding exists")
		return
	}

	// A binding runs one writer role (#382 §5.3). Resolve it against this
	// server's own registry -- the client's roles.json never travels -- and
	// refuse before InitBare, so a refused create leaves no bare repo behind.
	role := relevo.NormRole(req.Role)
	if role != "" {
		if err := relevo.CheckWriterRole(rt, role); err != nil {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
			return
		}
	}

	repoRoot, err := s.repoRoot(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	bare := filepath.Join(repoRoot, req.RepoID+".git")
	if err := s.cfg.Git.InitBare(r.Context(), bare); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	now := s.cfg.Now()
	roleName := role
	if roleName == "" {
		roleName = "builder"
	}
	candidateToken, harnessKind := relevo.PickServedCandidateFor(rt, roleName, req.Candidate)

	tier, err := relevo.ResolveServedTierFor(rt, roleName, candidateToken, req.Tier)
	if err != nil {
		if errors.Is(err, relevo.ErrTierAboveMax) {
			offending := req.Tier
			format := "tier %s exceeds this server's max_tier %s; raise max_tier in the server's policy.json"
			if offending == "" {
				format = "policy tier." + roleName + " %s exceeds max_tier %s"
				offending = string(tier)
			}
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax,
				fmt.Sprintf(format, offending, rt.Policy.MaxTierOrDefault()))
			return
		}
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	authorName, authorEmail := "", ""
	if req.Author != nil {
		authorName, authorEmail = req.Author.Name, req.Author.Email
	}

	cwd := rt.Store.WorktreePath(req.Name)
	b := store.Binding{
		Name:             req.Name,
		Owner:            string(caller),
		CWD:              cwd,
		Worktree:         cwd,
		Branch:           "relevo/" + req.Name,
		Base:             req.BaseCommit,
		Repo:             bare,
		Builder:          store.Endpoint{Kind: harnessKind, Mode: store.ModeHeadless, AgentName: req.Name},
		BuilderCandidate: candidateToken,
		Tier:             string(tier),
		Role:             role,
		Round:            1,
		State:            store.StateActive,
		RoundCap:         req.RoundCap,
		RoundTimeoutMS:   req.RoundTimeoutMS,
		Serve: &store.ServeFacts{
			RepoID:      req.RepoID,
			BareRepo:    bare,
			LastSeen:    now,
			AuthorName:  authorName,
			AuthorEmail: authorEmail,
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
	writeJSON(w, http.StatusCreated, relevo.ServedView(b, entries))
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

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
		views = append(views, relevo.ServedView(b, entries))
	}

	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "get", b) {
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
	view := relevo.ServedView(b, entries)
	view.Queue = s.queuePositionView(b, view, caller)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "done", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	switch relevo.RoundStateOf(b, entries) {
	case remote.RoundRunning:
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, "round is open")
		return
	case remote.RoundQueued:
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, fmt.Sprintf("round %d is queued; relevo stop to drop it from the queue, or unbind", b.Round))
		return
	}

	_, err = relevo.Done(r.Context(), rt, name)
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
	writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
}

func (s *Server) handleUnbind(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "unbind", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	_, err = relevo.Unbind(r.Context(), rt, name, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleStop ends the binding's open round on the server, leaving the binding
// in place (#344). An idle or already-closed binding is 409 nothing_to_stop, a
// halted one is 409 round_halted (message is the binding's Halt), and another
// client's binding is 404 -- the states the verbs around it refuse, plus
// nothing_to_stop.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "stop", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)
	switch relevo.RoundStateOf(b, entries) {
	case remote.RoundIdle, remote.RoundClosed:
		writeErr(w, http.StatusConflict, remote.CodeNothingToStop, relevo.ErrNothingToStop.Error())
		return
	case remote.RoundNeedsYou:
		writeErr(w, http.StatusConflict, remote.CodeRoundHalted, b.Halt)
		return
	}

	if _, err := relevo.Stop(r.Context(), rt, name, relevo.StopOptions{}); err != nil {
		if errors.Is(err, relevo.ErrNothingToStop) {
			writeErr(w, http.StatusConflict, remote.CodeNothingToStop, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "resume", b) {
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
	writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
}

func (s *Server) handleUnavailable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	name := r.PathValue("name")
	if name != "" {
		b, _, err := s.loadBinding(caller, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
			return
		}
		if !Allowed(caller, "unavailable", b) {
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

	if _, err := relevo.Unavailable(rt, req.Token, time.Time{}, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleAvailable lifts the server-wide ledger's rate-limit gate on a
// subject's provider. It is handleUnavailable minus the binding-scoped
// branch: the ledger is server-wide, so there is nothing binding-scoped to
// check and no /v1/bindings/{name}/available route. relevo.Available itself
// decides what the subject names (#301): a bare provider that gates nothing
// is still a 200 with Removed 0 when it is known, while a subject relevo
// knows nothing about is a 422 carrying the local verb's message.
func (s *Server) handleAvailable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	var req remote.AvailableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
		return
	}
	if req.Subject == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "subject is required")
		return
	}

	provider, removed, err := relevo.Available(rt, req.Subject, relevo.ClearedByPlanner)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, remote.AvailableResponse{Provider: provider, Removed: removed})
}
