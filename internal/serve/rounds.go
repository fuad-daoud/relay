package serve

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// sameSavedPlan reports whether planText is byte-identical to the plan the
// server saved for round -- the sha256 comparison the closed-round dedupe has
// used since the round-resend rule, factored out for the open-round idempotent
// send (#373 §4.2). A plan that was never saved, or cannot be read, is not the
// same plan.
func sameSavedPlan(rt relevo.Runtime, name string, round int, planText string) bool {
	saved, err := os.ReadFile(rt.Store.PlanPath(name, round))
	if err != nil {
		return false
	}
	return sha256.Sum256([]byte(planText)) == sha256.Sum256(saved)
}

func (s *Server) handleStartRound(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.MaxBundleBytes); err != nil {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	roundStr := r.FormValue("round")
	if roundStr == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "round is required")
		return
	}
	reqRound, err := strconv.Atoi(roundStr)
	if err != nil || reqRound < 1 {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "invalid round")
		return
	}

	planText := r.FormValue("plan")
	if planText == "" {
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "plan is required")
		return
	}

	tierStr := r.FormValue("tier")
	if tierStr != "" {
		if _, err := harness.ParseTier(tierStr); err != nil {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error())
			return
		}
	}

	// candidate is --builder's token (#318): a canonical candidate that
	// persists as the binding's builder from this round on. Absent or "" keeps
	// the binding's builder.
	candidate := r.FormValue("candidate")

	var bundlePart io.Reader
	file, _, fileErr := r.FormFile("bundle")
	if fileErr == nil {
		defer file.Close()
		size, seekErr := file.Seek(0, io.SeekEnd)
		if seekErr == nil && size > 0 {
			if _, err := file.Seek(0, io.SeekStart); err == nil {
				bundlePart = file
			}
		}
	}

	s.mu.Lock()

	caller := callerOf(r)
	name := r.PathValue("name")

	b, rt, err := s.loadBinding(caller, name)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "rounds", b) {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	entries, _ := rt.Store.ReadLog(name)

	// An identical retry of the open round is a no-op 200 (#373 §4.2). The
	// client's StartRound retries once the server advertises
	// FeatureIdempotentSend, so a repeated request for the round that is
	// running or queued, with the plan the server saved for it, must not
	// re-Send, reset QueuedAt, append a log entry or absorb the bundle
	// again. It returns before the running->409 check below; a different plan
	// for the open round is still 409 round_open.
	st := relevo.RoundStateOf(b, entries)
	if (st == remote.RoundRunning || st == remote.RoundQueued) && reqRound == b.Round && sameSavedPlan(rt, name, b.Round, planText) {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
		return
	}
	if st == remote.RoundRunning {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, "round is running")
		return
	}

	if reqRound != b.Round {
		if reqRound == b.Round-1 && sameSavedPlan(rt, name, b.Round-1, planText) {
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
			return
		}
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, remote.CodeRoundStarted, fmt.Sprintf("round %d already started with a different plan", reqRound))
		return
	}

	if b.Serve == nil || b.Serve.BareRepo == "" {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "missing bare repo facts")
		return
	}

	// A requested builder change is validated before anything moves: a bad
	// token refuses with nothing absorbed and no ref touched (#318 §5.4). Send
	// re-resolves it under its own lock as a backstop.
	if candidate != "" {
		if _, err := relevo.ResolveSendBuilderFor(rt, relevo.BindingRole(b), b.BuilderCandidate, candidate); err != nil {
			s.mu.Unlock()
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, err.Error())
			return
		}
	}

	bare := b.Serve.BareRepo
	outRef := "refs/relevo/" + name + "/out"

	// Absorb OUTSIDE s.mu
	s.mu.Unlock()

	if bundlePart != nil {
		_, absorbErr := s.transport.Absorb(r.Context(), bare, remote.ContentTypeGitBundle, bundlePart, []string{outRef})
		if absorbErr != nil {
			if errors.Is(absorbErr, git.ErrNotFastForward) ||
				errors.Is(absorbErr, git.ErrBadBundle) ||
				errors.Is(absorbErr, remote.ErrUnexpectedRef) ||
				errors.Is(absorbErr, remote.ErrUnsupportedType) {
				writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, absorbErr.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, "", absorbErr.Error())
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}

	outSHA, ok, refErr := s.cfg.Git.RefSHA(r.Context(), bare, outRef)
	if refErr != nil {
		writeErr(w, http.StatusInternalServerError, "", refErr.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "no outbound ref; send a bundle first")
		return
	}

	// The client's tags ship as data beside the bundle (#242). Set the ones
	// whose commit is already here; a tag for a commit the server does not
	// have is expected (an unrelated tag) and is skipped. Idempotent: a tag
	// already at that sha is a no-op, and old = "" means unconditional, so a
	// tag the client moved moves here too.
	if rawTags := r.FormValue("tags"); rawTags != "" {
		var tags []remote.TagRef
		if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
			writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "tags: "+err.Error())
			return
		}
		for _, tag := range tags {
			if err := validTagRef(tag); err != nil {
				slog.Debug("tag skipped", "tag", tag.Name, "err", err)
				continue
			}
			if err := s.cfg.Git.UpdateRef(r.Context(), bare, "refs/tags/"+tag.Name, tag.SHA, ""); err != nil {
				slog.Debug("tag not set", "tag", tag.Name, "err", err)
				continue
			}
		}
	}

	if _, statErr := os.Stat(b.Worktree); os.IsNotExist(statErr) {
		if err := s.cfg.Git.UpdateRef(r.Context(), bare, "refs/heads/"+b.Branch, outSHA, ""); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
		if err := s.cfg.Git.CheckoutWorktree(r.Context(), bare, b.Worktree, b.Branch); err != nil {
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
	} else {
		if err := s.cfg.Git.MergeFF(r.Context(), b.Worktree, outRef); err != nil {
			if errors.Is(err, git.ErrNotFastForward) {
				writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "relevo/"+name+" on the server has moved past your copy")
				return
			}
			if errors.Is(err, git.ErrMergeConflict) {
				writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "uncommitted work in the server worktree conflicts with your update")
				return
			}
			writeErr(w, http.StatusInternalServerError, "", err.Error())
			return
		}
	}

	tmpFile, err := os.CreateTemp(filepath.Join(s.cfg.Root, "tmp"), "plan-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	tmpFilePath := tmpFile.Name()
	defer os.Remove(tmpFilePath)

	if _, err := tmpFile.WriteString(planText); err != nil {
		_ = tmpFile.Close()
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	_ = tmpFile.Close()

	_, sendErr := relevo.Send(r.Context(), rt, name, tmpFilePath, relevo.SendOptions{Tier: tierStr, Builder: candidate, Defer: true})
	if sendErr != nil {
		if errors.Is(sendErr, relevo.ErrRunnerUnavailable) {
			writeErr(w, http.StatusServiceUnavailable, remote.CodeNoRunner, sendErr.Error())
			return
		}
		if errors.Is(sendErr, relevo.ErrBuilderBusy) {
			writeErr(w, http.StatusConflict, remote.CodeRoundOpen, sendErr.Error())
			return
		}
		if errors.Is(sendErr, relevo.ErrTierAboveMax) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax, sendErr.Error())
			return
		}
		// Backstop: the token was validated before absorb, but the ledger or
		// the candidates could have changed in between (#318).
		if errors.Is(sendErr, relevo.ErrBadBuilder) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeInvalid, sendErr.Error())
			return
		}
		if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
			b = reloaded
		}
		if b.State == store.StateNeedsYou || b.Halt != "" {
			slog.Warn("round start failed", "binding", name, "round", b.Round, "err", sendErr, "halt", b.Halt)
			writeErr(w, http.StatusConflict, remote.CodeRoundHalted, orText(b.Halt, sendErr.Error()))
			return
		}
		writeErr(w, http.StatusInternalServerError, "", sendErr.Error())
		return
	}

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}

	// The accept-time census, logged before admit: Send's deferred branch
	// does not know the census, so handleStartRound writes the audit entry
	// itself, still under s.mu (#285). If admit below starts the round at
	// once, the log shows "queued (0/3 busy)" followed by "started after 0s
	// queued" -- intended, the log is the audit trail.
	acceptCensus, censusErr := s.census()
	if censusErr != nil {
		slog.Warn("census failed at round accept", "binding", name, "err", censusErr)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindQueue, Confirmed: true,
		Note: fmt.Sprintf("queued (%d/%d builders busy)", acceptCensus.Running, s.cap()),
	}); err != nil {
		slog.Warn("append queue entry failed", "binding", name, "err", err)
	}

	if s.cfg.Hooks != nil {
		s.cfg.Hooks.Dispatch(r.Context(), hooks.Event{
			Type:      hooks.EventRoundQueued,
			BindingID: name,
			State:     string(b.State),
			Round:     b.Round,
			Timestamp: rt.Now().UTC(),
		})
	}

	if err := s.admit(r.Context()); err != nil {
		slog.Warn("admit failed", "binding", name, "err", err)
	}

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	view := relevo.ServedView(b, entries)
	view.Queue = s.queuePositionView(b, view, caller)
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleRoundFile(w http.ResponseWriter, r *http.Request) {
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
	if !Allowed(caller, "files", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > b.Round {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	kind := r.PathValue("kind")
	var path string
	switch kind {
	case "report":
		path = rt.Store.ReportPath(name, n)
	case "diff":
		path = rt.Store.DiffPath(name, n)
	case "log":
		path = rt.Store.BuilderLogPath(name, n)
	case "stream":
		path = rt.Store.BuilderStreamPath(name, n)
	case "plan":
		path = rt.Store.PlanPath(name, n)
	default:
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "unknown file kind")
		return
	}

	if kind != "log" {
		if b.Serve == nil || n > b.Serve.ClosedRound {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, fmt.Sprintf("round %d is not closed", n))
			return
		}
	}

	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "file not found")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// validTagRef reports whether a shipped tag is well formed enough to set as a
// ref. Git tag names may legally contain "/" (release/1.0, v1/rc2), so this
// follows git's ref-name rules loosely rather than refusing any slash; a
// name that still fails is skipped by the caller, never a 400 for the whole
// round start (#242 follow-up).
func validTagRef(tag remote.TagRef) error {
	name := tag.Name
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid tag name %q", name)
		}
	}
	if strings.Contains(name, "@{") || strings.ContainsAny(name, `\~^:?*[`) {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return fmt.Errorf("invalid tag name %q", name)
		}
	}
	if len(tag.SHA) != 40 || strings.Trim(tag.SHA, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("invalid tag sha %q", tag.SHA)
	}
	return nil
}

func (s *Server) handleRoundBundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()

	caller := callerOf(r)
	name := r.PathValue("name")
	b, _, err := s.loadBinding(caller, name)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}
	if !Allowed(caller, "bundle", b) {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || b.Serve == nil || n != b.Serve.ClosedRound {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "round not closed")
		return
	}

	bare := b.Serve.BareRepo
	refs := []string{"refs/heads/" + b.Branch}
	if b.Serve.DirtyCommit != "" {
		refs = append(refs, fmt.Sprintf("refs/relevo/%s/round-%d", name, n))
	}
	since := r.URL.Query().Get("since")

	s.mu.Unlock()

	snap, err := s.transport.Snapshot(r.Context(), bare, refs, since)
	if err != nil {
		if errors.Is(err, remote.ErrSinceUnknown) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "since is not an ancestor of the result")
			return
		}
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if snap.Empty {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer snap.Body.Close()

	w.Header().Set("Content-Type", snap.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, snap.Body)
}

func (s *Server) handleAckRound(w http.ResponseWriter, r *http.Request) {
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
	if !Allowed(caller, "ack", b) {
		writeErr(w, http.StatusNotFound, remote.CodeNotFound, "not found")
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || b.Serve == nil || n > b.Serve.ClosedRound {
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, fmt.Sprintf("round %d is not closed", n))
		return
	}

	if n > b.Serve.AckedRound {
		b.Serve.AckedRound = n
	}
	if err := rt.Store.Save(b); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}

	if reloaded, err := rt.Store.Load(name); err == nil {
		b = reloaded
	}
	entries, _ := rt.Store.ReadLog(name)
	writeJSON(w, http.StatusOK, relevo.ServedView(b, entries))
}
