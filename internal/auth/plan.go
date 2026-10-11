package auth

import (
	"context"
	"log/slog"

	"github.com/xiaolai/recite/internal/codexapp"
)

// PlanStatus reads the Codex plan status through src's app-server client (OBS-04). The file
// source has no app-server, so the call is skipped with a warning and ok is false.
func PlanStatus(ctx context.Context, src TokenSource, logger *slog.Logger) (rl codexapp.RateLimits, ok bool, err error) {
	cs, isCodex := src.(*CodexSource)
	if !isCodex {
		if logger != nil {
			logger.Warn("Codex plan status skipped: it is read through the Codex app-server, and the auth source is file")
		}
		return codexapp.RateLimits{}, false, nil
	}
	rl, err = cs.planStatus(ctx)
	if err != nil {
		return codexapp.RateLimits{}, false, err
	}
	return rl, true, nil
}

func (s *CodexSource) planStatus(ctx context.Context) (codexapp.RateLimits, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.startLocked(ctx)
	if err != nil {
		return codexapp.RateLimits{}, err
	}
	return c.PlanStatus(ctx)
}
