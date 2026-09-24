package doctor

// GitIdentityInput is everything #335's git identity row needs that
// GitIdentityCheck cannot read itself: whether any remote server is
// configured, whether the working directory is inside a git work tree, and
// the identity resolved there. cmd/relevo gathers it; the rule lives here.
type GitIdentityInput struct {
	// HasServers is true when at least one remote server is configured.
	HasServers bool
	// InRepo is true when the working directory is inside a git work tree.
	InRepo bool
	// Name and Email are the resolved identity; "" when the key is unset.
	Name  string
	Email string
}

// GitIdentityCheck is the `relevo bind --server` preflight (#335): a remote
// builder commits as the client, so a repo whose effective user.name or
// user.email is unset makes every remote add refuse.
//
// ok is false, meaning no row, unless there is a server to add to and the
// working directory is inside a repo: a local-only machine has no remote
// builder to name, and outside a repo there is no identity to resolve. The
// row is global (Group ""), so it renders with the other machine-wide rows.
func GitIdentityCheck(in GitIdentityInput) (Check, bool) {
	if !in.HasServers || !in.InRepo {
		return Check{}, false
	}

	if in.Name != "" && in.Email != "" {
		return Check{
			Name:     "git identity",
			Severity: SevOK,
			Detail:   in.Name + " <" + in.Email + ">",
		}, true
	}

	detail := "user.name not set"
	switch {
	case in.Name == "" && in.Email == "":
		detail = "user.name/user.email not set: relevo bind --server will refuse"
	case in.Email == "":
		detail = "user.email not set"
	}
	return Check{
		Name:     "git identity",
		Severity: SevWarn,
		Detail:   detail,
		Fix:      "git config --global user.name '<your name>' && git config --global user.email '<you@example.com>'",
	}, true
}
