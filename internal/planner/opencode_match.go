package planner

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OpencodeSession is one row from OpenCode's session table (§3).
type OpencodeSession struct {
	ID        string
	Directory string
	ParentID  string
	Title     string
	Updated   time.Time
	Archived  bool
}

// ErrNoOpencodeSession is returned when no OpenCode session matches the working directory (§3).
var ErrNoOpencodeSession = errors.New("no matching OpenCode session")

// ErrAmbiguousOpencodeSession is returned when multiple active OpenCode sessions match the directory (§3).
type ErrAmbiguousOpencodeSession struct {
	Dir    string
	Titles []string
}

func (e ErrAmbiguousOpencodeSession) Error() string {
	return fmt.Sprintf("two OpenCode sessions are active in %s: %s; pass --planner", e.Dir, strings.Join(e.Titles, ", "))
}

func (e ErrAmbiguousOpencodeSession) Is(target error) bool {
	other, ok := target.(ErrAmbiguousOpencodeSession)
	if !ok {
		return false
	}
	if e.Dir != other.Dir || len(e.Titles) != len(other.Titles) {
		return false
	}
	for i := range e.Titles {
		if e.Titles[i] != other.Titles[i] {
			return false
		}
	}
	return true
}

// MatchOpencodeSession matches cwd against sessions (§4).
// Pure. Pre: cwd absolute (clean it with filepath.Clean). Post, in order:
//  1. Keep sessions with ParentID == "", !Archived, and Directory equal to
//     cwd or an ancestor of it (path-segment aware: /a/b is an ancestor of
//     /a/b/c, not of /a/bc).
//  2. Of those, keep only the ones with the longest Directory.
//  3. None left -> ErrNoOpencodeSession.
//  4. Sort by Updated descending. If there are >= 2 and the second's Updated is
//     within 60 s of now (now.Sub(second.Updated) <= 60*time.Second) ->
//     ErrAmbiguousOpencodeSession{Dir, Titles: [first.Title, second.Title]}.
//  5. Else the first's ID.
func MatchOpencodeSession(cwd string, sessions []OpencodeSession, now time.Time) (string, error) {
	cleanCWD := filepath.Clean(cwd)

	var matched []OpencodeSession
	for _, s := range sessions {
		if s.ParentID != "" || s.Archived {
			continue
		}
		s.Directory = filepath.Clean(s.Directory)
		if isAncestorOrEqual(s.Directory, cleanCWD) {
			matched = append(matched, s)
		}
	}

	if len(matched) == 0 {
		return "", ErrNoOpencodeSession
	}

	// 2. Of those, keep only the ones with the longest Directory.
	maxLen := -1
	for _, s := range matched {
		l := len(s.Directory)
		if l > maxLen {
			maxLen = l
		}
	}

	var longest []OpencodeSession
	for _, s := range matched {
		if len(s.Directory) == maxLen {
			longest = append(longest, s)
		}
	}

	if len(longest) == 0 {
		return "", ErrNoOpencodeSession
	}

	// 4. Sort by Updated descending.
	sort.SliceStable(longest, func(i, j int) bool {
		return longest[i].Updated.After(longest[j].Updated)
	})

	if len(longest) >= 2 && now.Sub(longest[1].Updated) <= 60*time.Second {
		return "", ErrAmbiguousOpencodeSession{
			Dir:    longest[0].Directory,
			Titles: []string{longest[0].Title, longest[1].Title},
		}
	}

	return longest[0].ID, nil
}

func isAncestorOrEqual(dir, cwd string) bool {
	if dir == cwd {
		return true
	}
	if dir == "/" {
		return true
	}
	return strings.HasPrefix(cwd, dir+"/")
}
