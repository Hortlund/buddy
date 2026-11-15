package tasks

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
)

func activeCheckoutOverlap(ctx context.Context, db queryer, host, job, workspace string, edits bool) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.workspace FROM worker_sessions s JOIN tasks t ON t.id=s.task_id
		JOIN chat_turns c ON c.task_id=t.id WHERE s.host_id=? AND s.task_id<>?
		AND s.ended IS NULL AND t.status IN ('running','cancelling') AND (? OR c.allow_edits=1)`, host, job, edits)
	if err != nil {
		return false, err
	}
	var workspaces []string
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			rows.Close()
			return false, err
		}
		if other != "" {
			workspaces = append(workspaces, other)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, other := range workspaces {
		if checkoutPathsOverlap(workspace, other) {
			return true, nil
		}
		// Remote paths describe another machine's filesystem. Only resolve
		// physical directory aliases for sessions running on Buddy Home.
		if host == "server" {
			overlap, err := WorkspacePathsOverlap(workspace, other)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			if overlap {
				return true, nil
			}
		}
	}
	return false, nil
}

func checkoutPathsOverlap(first, second string) bool {
	// macOS and Linux workers report absolute slash-separated paths. Keep
	// path comparisons independent of the filesystem hosting Buddy Home.
	first, second = path.Clean(first), path.Clean(second)
	return first == second || strings.HasPrefix(first, strings.TrimSuffix(second, "/")+"/") ||
		strings.HasPrefix(second, strings.TrimSuffix(first, "/")+"/")
}
