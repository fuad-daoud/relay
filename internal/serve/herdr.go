package serve

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fuad-daoud/relay/internal/herdr"
)

var errNoHerdr = errors.New("relay serve has no herdr")

type stubHerdr struct{}

func (stubHerdr) ListAgents(ctx context.Context) ([]herdr.Agent, error) {
	return nil, nil
}

func (stubHerdr) Notify(ctx context.Context, title, body string, sound herdr.Sound) error {
	slog.Info("notify", "title", title, "body", body, "sound", sound)
	return nil
}

func (stubHerdr) ReportMetadata(ctx context.Context, paneID string, m herdr.PaneMetadata) error {
	return nil
}

func (stubHerdr) Prompt(ctx context.Context, target, text string) error {
	return errNoHerdr
}

func (stubHerdr) SendKeys(ctx context.Context, target, keys string) error {
	return errNoHerdr
}

func (stubHerdr) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	return "", errNoHerdr
}

func (stubHerdr) ReadAgentSource(ctx context.Context, target, source string, lines int) (string, error) {
	return "", errNoHerdr
}

func (stubHerdr) CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error) {
	return "", errNoHerdr
}

func (stubHerdr) StartAgent(ctx context.Context, name, kind, paneID string, args []string) error {
	return errNoHerdr
}

func (stubHerdr) ClosePane(ctx context.Context, paneID string) error {
	return errNoHerdr
}
