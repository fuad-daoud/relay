package relevo

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/planner"
)

type fakeFinderExec struct {
	out []byte
	err error
}

func (f *fakeFinderExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestOpencodeSessionFinder(t *testing.T) {
	now := time.Now()

	t.Run("JSON array with time_updated in ms and null parent_id/time_archived -> right id", func(t *testing.T) {
		jsonOut := `[
			{
				"id": "ses_target",
				"directory": "/path/to/project",
				"parent_id": null,
				"title": "My Session",
				"time_updated": ` + strconv.FormatInt(now.UnixMilli(), 10) + `,
				"time_archived": null
			}
		]`
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{out: []byte(jsonOut)},
			DBPath: "/dummy/opencode.db",
		}
		id, err := finder.Find("/path/to/project/subdir", now)
		if err != nil {
			t.Fatalf("Find failed: %v", err)
		}
		if id != "ses_target" {
			t.Errorf("Find = %q, want ses_target", id)
		}
	})

	t.Run("empty output -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{out: []byte("")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, planner.ErrNoOpencodeSession) {
			t.Errorf("Find on empty output: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})

	t.Run("exec error -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{err: errors.New("command failed")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, planner.ErrNoOpencodeSession) {
			t.Errorf("Find on exec error: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})

	t.Run("decode error -> ErrNoOpencodeSession", func(t *testing.T) {
		finder := OpencodeSessionFinder{
			Exec:   &fakeFinderExec{out: []byte("invalid json")},
			DBPath: "/dummy/opencode.db",
		}
		_, err := finder.Find("/path/to/project", now)
		if !errors.Is(err, planner.ErrNoOpencodeSession) {
			t.Errorf("Find on decode error: err = %v, want errors.Is ErrNoOpencodeSession", err)
		}
	})
}
