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

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

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
	if relay.RoundStateOf(b, entries) == remote.RoundRunning {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, remote.CodeRoundOpen, "round is running")
		return
	}

	if reqRound != b.Round {
		if reqRound == b.Round-1 {
			planPath := rt.Store.PlanPath(name, b.Round-1)
			savedPlan, readErr := os.ReadFile(planPath)
			if readErr == nil {
				h1 := sha256.Sum256([]byte(planText))
				h2 := sha256.Sum256(savedPlan)
				if h1 == h2 {
					s.mu.Unlock()
					writeJSON(w, http.StatusOK, relay.ServedView(b, entries))
					return
				}
			}
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

	bare := b.Serve.BareRepo
	outRef := "refs/relay/" + name + "/out"

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
				writeErr(w, http.StatusBadRequest, remote.CodeInvalid, "tags: "+err.Error())
				return
			}
		}
		for _, tag := range tags {
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
				writeErr(w, http.StatusUnprocessableEntity, remote.CodeNotFastForward, "relay/"+name+" on the server has moved past your copy")
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

	_, sendErr := relay.Send(r.Context(), rt, name, tmpFilePath, relay.SendOptions{Tier: tierStr})
	if sendErr != nil {
		if errors.Is(sendErr, relay.ErrRunnerUnavailable) {
			writeErr(w, http.StatusServiceUnavailable, remote.CodeNoRunner, sendErr.Error())
			return
		}
		if errors.Is(sendErr, relay.ErrBuilderBusy) {
			writeErr(w, http.StatusConflict, remote.CodeRoundOpen, sendErr.Error())
			return
		}
		if errors.Is(sendErr, relay.ErrTierAboveMax) {
			writeErr(w, http.StatusUnprocessableEntity, remote.CodeTierAboveMax, sendErr.Error())
			return
		}
		if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
			b = reloaded
		}
		if b.State == store.StateNeedsYou || b.Halt != "" {
			entries, _ = rt.Store.ReadLog(name)
			writeJSON(w, http.StatusCreated, relay.ServedView(b, entries))
			return
		}
		writeErr(w, http.StatusInternalServerError, "", sendErr.Error())
		return
	}

	if reloaded, loadErr := rt.Store.Load(name); loadErr == nil {
		b = reloaded
	}
	entries, _ = rt.Store.ReadLog(name)
	writeJSON(w, http.StatusCreated, relay.ServedView(b, entries))
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
// ref: a non-empty name with no slash, "..", or whitespace, and a 40-hex
// commit sha (#242).
func validTagRef(tag remote.TagRef) error {
	if tag.Name == "" || strings.Contains(tag.Name, "..") || strings.ContainsAny(tag.Name, "/ \t\n\r") {
		return fmt.Errorf("invalid tag name %q", tag.Name)
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
		refs = append(refs, fmt.Sprintf("refs/relay/%s/round-%d", name, n))
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
	writeJSON(w, http.StatusOK, relay.ServedView(b, entries))
}
