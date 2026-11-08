package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"
)

// Upload while the job still owns its lease. Finishing first would make the
// server reject a retry, even if a temporary network failure has recovered.
func returnWorkArtifacts(ctx context.Context, outbox string, upload func(context.Context, tasks.WorkArtifact) error) error {
	items, err := tasks.CollectWorkArtifacts(outbox)
	if err != nil {
		return fmt.Errorf("couldn't collect deliverables: %w", err)
	}
	for _, artifact := range items {
		for attempt := 0; ; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := upload(ctx, artifact)
			if err == nil {
				break
			}
			var remote *api.HTTPError
			permanent := errors.As(err, &remote) && remote.Status < 500 && remote.Status != http.StatusTooManyRequests && remote.Status != http.StatusRequestTimeout
			if permanent || attempt == 4 {
				return fmt.Errorf("couldn't return deliverable %q (the file remains on this worker): %w", artifact.Name, err)
			}
			timer := time.NewTimer(time.Second << attempt)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}
