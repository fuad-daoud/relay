//go:build !unix

package proc

// InheritedReaper has no implementation off unix: there is no wait4, and relevo
// does not re-exec there either. It keeps the same API so callers need no build
// tag of their own.
type InheritedReaper struct{}

// NewInheritedReaper returns a reaper that inherits nothing.
func NewInheritedReaper(self int, procRoot string) *InheritedReaper {
	return &InheritedReaper{}
}

// Reap never reaps anything.
func (r *InheritedReaper) Reap() []int { return nil }
