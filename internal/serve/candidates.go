package serve

import (
	"net/http"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
)

func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	rt, err := s.runtime(caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, remote.CodeInvalid, "malformed client id")
		return
	}

	gates := relay.Gates(rt)
	gatedMap := make(map[string]bool, len(gates))
	for _, g := range gates {
		gatedMap[g.Token] = true
	}

	pickedToken, _ := relay.PickServedCandidate(rt, "")

	var views []remote.CandidateView
	if s.cfg.Candidates != nil {
		for _, refStr := range s.cfg.Candidates.Refs() {
			ref, err := candidate.ParseRef(refStr)
			if err != nil {
				continue
			}
			c, err := s.cfg.Candidates.Lookup(ref)
			if err != nil {
				continue
			}
			token := c.Ref().String()
			views = append(views, remote.CandidateView{
				Token: token,
				Kind:  c.Harness,
				Gated: gatedMap[token],
				Pick:  token == pickedToken && pickedToken != "",
			})
		}
	}
	if views == nil {
		views = []remote.CandidateView{}
	}

	writeJSON(w, http.StatusOK, remote.CandidatesResponse{
		Candidates: views,
	})
}
