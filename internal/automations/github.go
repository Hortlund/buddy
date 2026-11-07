package automations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"buddy/internal/tasks"
)

type workflowRun struct {
	ID         int64     `json:"id"`
	Attempt    int64     `json:"run_attempt"`
	Name       string    `json:"name"`
	Branch     string    `json:"head_branch"`
	SHA        string    `json:"head_sha"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type githubProgress struct {
	Since   time.Time      `json:"since"`
	Until   time.Time      `json:"until"`
	Windows []githubWindow `json:"windows"`
}

type githubWindow struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Page       int       `json:"page,omitempty"`
	Offset     int       `json:"offset,omitempty"`
	RunID      int64     `json:"run_id,omitempty"`
	Attempt    int64     `json:"attempt,omitempty"`
	Unfiltered bool      `json:"unfiltered,omitempty"`
}

func cleanHeading(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\x00", "")), " ")
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}

func ciSignal(repo string, run workflowRun) tasks.StandingSignal {
	name := cleanHeading(run.Name, 180)
	if name == "" {
		name = "GitHub Actions"
	}
	return tasks.StandingSignal{
		Source: tasks.GitHubCI, Scope: repo, Key: tasks.CIWatchKey(run.ID, run.Attempt),
		Branch: cleanHeading(run.Branch, 250), Workflow: name,
		OccurredAt: run.UpdatedAt,
		Title:      name + " failed", URL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, run.ID),
		Detail: fmt.Sprintf("Repository: %s\nBranch: %s\nCommit: %s\nRun attempt: %d", repo,
			cleanHeading(run.Branch, 250), cleanHeading(run.SHA, 80), max(1, run.Attempt)),
	}
}

func (s *Service) GitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.config.GitHubWebhookSecret == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mac := hmac.New(sha256.New, []byte(s.config.GitHubWebhookSecret))
	mac.Write(data)
	signature, valid := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	got, err := hex.DecodeString(signature)
	if !valid || err != nil || !hmac.Equal(mac.Sum(nil), got) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "workflow_run" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var event struct {
		Action     string      `json:"action"`
		Run        workflowRun `json:"workflow_run"`
		Repository struct {
			Name string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(data, &event) != nil || event.Run.ID <= 0 || event.Repository.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if event.Action == "completed" && event.Run.Conclusion == "failure" {
		repo := strings.ToLower(event.Repository.Name)
		accepted, err := s.store.RecordStandingSignal(r.Context(), ciSignal(repo, event.Run))
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if accepted && s.config.GitHubToken == "" {
			_ = s.store.NoteStandingWebhook(r.Context(), repo)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) pollGitHub(ctx context.Context, w tasks.Watch, p tasks.PollState, token string) (string, string) {
	var progress githubProgress
	if strings.HasPrefix(p.Cursor, "{") {
		if err := json.Unmarshal([]byte(p.Cursor), &progress); err != nil {
			return "", "Invalid saved GitHub progress."
		}
	} else {
		progress.Until = time.Now().UTC()
		progress.Since = w.ActiveSince
		if since, err := time.Parse(time.RFC3339Nano, p.Cursor); err == nil && !since.Before(w.ActiveSince) {
			progress.Since = maxTime(w.ActiveSince, since.Add(-5*time.Minute))
		}
		progress.Windows = []githubWindow{{Start: progress.Since.AddDate(0, 0, -7).Truncate(time.Second), End: progress.Until.Truncate(time.Second), Page: 1}}
		if err := s.checkpointGitHub(ctx, p, progress); err != nil {
			return "", "Couldn’t save GitHub progress."
		}
	}
	base := githubAPI + "/repos/" + w.Scope + "/actions/runs"
	for requests := 0; requests < 10 && len(progress.Windows) != 0; requests++ {
		window := &progress.Windows[0]
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(window.Page)}}
		if !window.Unfiltered {
			query.Set("created", window.Start.Format(time.RFC3339)+".."+window.End.Format(time.RFC3339))
		}
		var response struct {
			Total int           `json:"total_count"`
			Runs  []workflowRun `json:"workflow_runs"`
		}
		if err := s.getJSON(ctx, base+"?"+query.Encode(), token, &response); err != nil {
			return "", "GitHub: " + err.Error()
		}
		if !window.Unfiltered && (response.Total >= 1000 || window.Page == 10 && len(response.Runs) == 100) {
			seconds := int64(window.End.Sub(window.Start) / time.Second)
			if seconds == 0 {
				// The GitHub cap applies to filtered searches. For a saturated
				// second, scan the unfiltered listing and match this range locally.
				*window = githubWindow{Start: window.Start, End: window.End, Page: 1, Unfiltered: true}
			} else {
				mid := window.Start.Add(time.Duration(seconds/2) * time.Second)
				progress.Windows = append([]githubWindow{{Start: window.Start, End: mid, Page: 1},
					{Start: mid.Add(time.Second), End: window.End, Page: 1}}, progress.Windows[1:]...)
			}
		} else {
			for window.Offset < len(response.Runs) {
				run := response.Runs[window.Offset]
				inWindow := !window.Unfiltered || run.CreatedAt.Truncate(time.Second).Equal(window.Start)
				if inWindow && run.ID > 0 && !run.UpdatedAt.Before(progress.Since) {
					if err := s.recordGitHubRun(ctx, p, progress, window, w, base, token, run); err != nil {
						return "", "GitHub: " + err.Error()
					}
				}
				window.Offset++
				window.RunID, window.Attempt = 0, 0
			}
			if len(response.Runs) < 100 {
				progress.Windows = progress.Windows[1:]
			} else {
				window.Page++
				window.Offset = 0
			}
		}
		if err := s.checkpointGitHub(ctx, p, progress); err != nil {
			return "", "Couldn’t save GitHub progress."
		}
	}
	if len(progress.Windows) == 0 {
		return progress.Until.Format(time.RFC3339Nano), ""
	}
	return "", "Catching up with GitHub. Progress is saved."
}

func (s *Service) checkpointGitHub(ctx context.Context, p tasks.PollState, progress githubProgress) error {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return s.store.CheckpointStandingPoll(ctx, p, string(data))
}

func (s *Service) recordGitHubRun(ctx context.Context, p tasks.PollState, progress githubProgress, window *githubWindow, w tasks.Watch, base, token string, run workflowRun) error {
	if run.Conclusion == "failure" {
		signal := ciSignal(w.Scope, run)
		signal.ConnectionID = w.ConnectionID
		if _, err := s.store.RecordStandingSignal(ctx, signal); err != nil {
			return err
		}
	}
	if window.RunID != run.ID {
		window.RunID, window.Attempt = run.ID, run.Attempt-1
	}
	for window.Attempt > 0 {
		number := window.Attempt
		seen, err := s.store.StandingSignalExists(ctx, tasks.WatchSourceKey(w.Source, w.ConnectionID), w.Scope, tasks.CIWatchKey(run.ID, number))
		if err != nil {
			return err
		}
		if !seen {
			var attempt workflowRun
			err := s.getJSON(ctx, fmt.Sprintf("%s/%d/attempts/%d", base, run.ID, number), token, &attempt)
			if err != nil && !errors.Is(err, responseError(http.StatusNotFound)) {
				return err
			}
			if err == nil && attempt.UpdatedAt.Before(progress.Since) {
				break
			}
			if err == nil && attempt.Conclusion == "failure" {
				attempt.ID, attempt.Attempt = run.ID, number
				signal := ciSignal(w.Scope, attempt)
				signal.ConnectionID = w.ConnectionID
				if _, err := s.store.RecordStandingSignal(ctx, signal); err != nil {
					return err
				}
			}
		}
		window.Attempt--
		if err := s.checkpointGitHub(ctx, p, progress); err != nil {
			return err
		}
	}
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Enrich fetches evidence with a server-owned read-only credential before the
// model sees the event. Its result is checkpointed independently of the model.
func (s *Service) Enrich(ctx context.Context, signal tasks.StandingSignal) (tasks.StandingSignal, error) {
	if signal.Source != tasks.GitHubCI || signal.Enriched {
		return signal, nil
	}
	token := s.config.GitHubToken
	if signal.ConnectionID != "" {
		c, err := s.activeConnection(ctx, signal.ConnectionID)
		if err != nil {
			return signal, nil
		}
		token = c.Secret
	}
	if token == "" {
		return signal, nil
	}
	run, attempt, _ := strings.Cut(signal.Key, ":")
	var response struct {
		Jobs []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Steps      []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	endpoint := githubAPI + "/repos/" + signal.Scope + "/actions/runs/" + run + "/attempts/" + attempt + "/jobs?per_page=100"
	if err := s.getJSON(ctx, endpoint, token, &response); err != nil {
		signal.Detail += "\nFailed-step details unavailable: " + err.Error()
		return signal, nil
	}
	var logJob int64
	for _, job := range response.Jobs {
		if job.Conclusion != "failure" {
			continue
		}
		signal.Detail += "\nFailed job: " + cleanHeading(job.Name, 200)
		for _, step := range job.Steps {
			if step.Conclusion == "failure" && len(signal.Detail) < 7500 {
				signal.Detail += "\n  Failed step: " + cleanHeading(step.Name, 200)
			}
		}
		if logJob == 0 {
			logJob = job.ID
		}
		if len(signal.Detail) >= 7500 {
			break
		}
	}
	if signal.ConnectionID == "" && s.config.GitHubLogs && logJob > 0 {
		if tail := s.githubLog(ctx, signal.Scope, logJob); tail != "" {
			signal.Detail += "\nCI log excerpt (untrusted):\n" + tail
		}
	}
	signal.Enriched = true
	return signal, s.store.CheckpointStandingEvidence(ctx, signal)
}

func (s *Service) githubLog(ctx context.Context, repo string, job int64) string {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/actions/jobs/%d/logs", repo, job)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+s.config.GitHubToken)
	res, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	link, err := res.Location()
	if res.StatusCode != http.StatusFound || err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil {
		return ""
	}
	download, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return ""
	}
	// The URL is already signed. Never forward the GitHub credential.
	download.Header.Set("Range", "bytes=-6000")
	result, err := s.client.Do(download)
	if err != nil {
		return ""
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK && result.StatusCode != http.StatusPartialContent {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, 6000))
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(string(data), "")
}
