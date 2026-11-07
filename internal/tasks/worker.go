package tasks

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	PrepareWatchStart func(context.Context, *Watch) error
	ChooseGroupBud    func(context.Context, GroupRouting) string
	ApplyGitHubWrite  func(context.Context, GitHubWrite, string) (GitHubResult, error)
	store             *Store
	run               Runner
	agent             AgentRunner
	wake              chan struct{}
	background        *Worker
	workLane          bool

	mu   sync.Mutex
	runs map[string]*workerRun
}

type workerRun struct {
	repository string
	cancel     context.CancelFunc
	steerWake  chan struct{}
	steerReady bool
	completing bool
}

const localBackgroundSlots = 3

func NewWorker(store *Store, runner Runner, agent AgentRunner) *Worker {
	w := &Worker{store: store, run: runner, agent: agent, wake: make(chan struct{}, 1), runs: make(map[string]*workerRun)}
	w.background = &Worker{store: store, agent: agent, wake: make(chan struct{}, 1), workLane: true, runs: make(map[string]*workerRun)}
	return w
}

func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) WakeBackground() {
	if w.background != nil {
		w.background.Wake()
	}
}

func (w *Worker) lane(ctx context.Context, id string) (*Worker, error) {
	t, err := w.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Kind == BackgroundWork && w.background != nil {
		return w.background, nil
	}
	return w, nil
}

func (w *Worker) Cancel(ctx context.Context, id string, queuedOnly bool) (Task, error) {
	lane, err := w.lane(ctx, id)
	if err != nil {
		return Task{}, err
	}
	return lane.cancelTask(ctx, id, queuedOnly)
}

func (w *Worker) cancelTask(ctx context.Context, id string, queuedOnly bool) (Task, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[id]
	if run != nil && run.completing {
		return Task{}, ErrInactive
	}
	t, err := w.store.cancel(ctx, id, queuedOnly)
	if err == nil {
		for job, running := range w.runs {
			if task, lookupErr := w.store.Get(ctx, job); lookupErr == nil && task.Status == Cancelling {
				running.cancel()
			}
		}
	}
	return t, err
}

// Run takes its lifetime from the server, never from the submitting HTTP request.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range localBackgroundSlots {
		wg.Go(func() { w.background.runLane(ctx) })
	}
	wg.Go(func() { w.runSchedules(ctx) })
	w.runLane(ctx)
	wg.Wait()
}

func (w *Worker) runSchedules(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.store.ExpireDeviceJobs(ctx); err != nil && ctx.Err() == nil {
			log.Printf("expire device jobs: %v", err)
		}
		queued, err := w.store.enqueueDueRoutine(ctx, time.Now())
		if err != nil && !errors.Is(err, ErrQueueFull) && ctx.Err() == nil {
			log.Printf("schedule routine: %v", err)
		}
		standing, standingErr := w.store.enqueueStandingWatch(ctx)
		if standing || queued {
			w.background.Wake()
			continue
		}
		if standingErr != nil && !errors.Is(standingErr, ErrQueueFull) && ctx.Err() == nil {
			log.Printf("schedule watch: %v", standingErr)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) runLane(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		w.mu.Lock()
		var occupied []string
		for _, run := range w.runs {
			if run.repository != "" {
				occupied = append(occupied, run.repository)
			}
		}
		t, err := w.store.claimLane(ctx, w.workLane, occupied...)
		if err == nil {
			job, cancel := context.WithTimeout(ctx, time.Duration(t.TimeoutSeconds)*time.Second)
			run := &workerRun{repository: t.Repository, cancel: cancel, steerWake: make(chan struct{}, 1)}
			w.runs[t.ID] = run
			w.mu.Unlock()
			if w.workLane {
				w.Wake()
			}

			emit := func(stream, text string) error {
				if stream == "activity" {
					if err := w.store.append(context.Background(), t.ID, stream, text); err != nil {
						return err
					}
				}
				if stream == "activity" || stream == "progress" {
					_, err := w.store.db.ExecContext(context.Background(),
						"UPDATE tasks SET summary=? WHERE id=? AND status='running'", text, t.ID)
					return err
				}
				return w.store.append(context.Background(), t.ID, stream, text)
			}
			var action TurnAction
			reply, runErr := w.execute(job, t, run, emit, &action)
			cancel()
			w.finish(ctx, t, reply, runErr, &action)
			continue
		}
		w.mu.Unlock()
		if !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			log.Printf("claim task: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *Worker) execute(ctx context.Context, t Task, run *workerRun, emit Emit, action *TurnAction) (string, error) {
	if t.Kind != AgentTurn && t.Kind != BackgroundWork {
		return "", w.run(ctx, t, emit)
	}
	if t.Kind == AgentTurn {
		proposal, revision, handled, err := w.store.confirmedGitHubWrite(ctx, t.ID)
		if err != nil {
			return "", err
		}
		if handled {
			if revision == "" {
				return "GitHub change cancelled.", nil
			}
			if w.ApplyGitHubWrite == nil {
				return "", &InputError{"GitHub collaboration is unavailable on this server."}
			}
			w.mu.Lock()
			err = ctx.Err()
			if err == nil {
				// An outbound write may already have been applied when the
				// server loses the response. Do not offer to cancel it.
				run.completing = true
			}
			w.mu.Unlock()
			if err != nil {
				return "", err
			}
			action.githubWriteRun = true
			_ = emit("commentary", "Applying the approved change on GitHub…")
			result, err := w.ApplyGitHubWrite(ctx, proposal, revision)
			if err != nil {
				return "", err
			}
			action.githubResult = &result
			return "", nil
		}
		plan, schedule, confirmed, err := w.store.confirmedManagement(ctx, t.ID)
		if err != nil {
			return "", err
		}
		if confirmed {
			action.Manage, action.managementConfirmed = plan, true
			action.approvedSchedule = schedule
			return "", w.prepareManagement(ctx, t.ID, action)
		}
		t, err = w.routeGroupTurn(ctx, t)
		if err != nil {
			return "", err
		}
	}
	if w.agent == nil {
		return "", errors.New("The server has no agent runner configured.")
	}
	turn, err := w.store.turn(ctx, t)
	if err != nil {
		return "", err
	}
	if t.Kind == BackgroundWork && turn.WatchSignal == nil {
		var branch string
		turn.Agent.Workspace, branch, err = PrepareProjectWorkspace(ctx, turn.Agent.Workspace, t.ID, turn.ProjectID, turn.Agent.AllowEdits)
		if err != nil {
			return "", err
		}
		if err = w.store.StartWorkerSession(ctx, t.ID, "server", turn.Agent.Workspace, branch); err != nil {
			return "", err
		}
		if turn.Agent.AllowEdits && turn.Agent.Workspace != "" {
			turn.OutputDirectory, err = ArtifactOutbox(turn.Agent.Workspace, t.ID)
			if err != nil {
				return "", err
			}
		}
	}
	turn.action = action
	turn.steering = &turnSteering{
		wake: run.steerWake,
		ready: func(ready bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if !run.completing {
				run.steerReady = ready
			}
		},
		next: func(ctx context.Context) (SteeringMessage, error) { return w.store.nextSteering(ctx, t.ID) },
		mark: w.store.markSteering,
	}
	reply, err := w.agent(ctx, turn, emit)
	if err == nil && turn.OutputDirectory != "" {
		artifacts, collectErr := CollectWorkArtifacts(turn.OutputDirectory)
		if collectErr != nil {
			_ = emit("system", "Could not collect artifacts: "+collectErr.Error())
		}
		for _, artifact := range artifacts {
			if saveErr := w.store.SaveWorkArtifact(ctx, t.ID, "", "", artifact); saveErr != nil {
				_ = emit("system", "Could not save artifact: "+artifact.Name)
			}
		}
	}
	for _, plan := range append(slices.Clone(action.ManagementChoices), action.Manage) {
		for i := range plan {
			if plan[i].Operation == "edit_soul" {
				plan[i].Revision = turn.soulRevision
			}
		}
	}
	if turn.earlierAttachments {
		action.earlierAttachmentSource = turn.attachmentSource
	}
	return reply, err
}

func (w *Worker) finish(server context.Context, t Task, reply string, runErr error, action *TurnAction) {
	w.mu.Lock()
	run := w.runs[t.ID]
	run.completing, run.steerReady = true, false
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.runs, t.ID)
		w.mu.Unlock()
	}()
	status, summary := Succeeded, "Repository check passed."
	switch {
	case runErr == nil:
	case server.Err() != nil:
		status, summary = Interrupted, "The server stopped before this check finished. Run it again to retry."
	case t.Kind == BackgroundWork && errors.Is(runErr, ErrWorkNeedsInput) && strings.TrimSpace(reply) != "":
		status, summary = NeedsInput, "Waiting for your reply."
	case errors.Is(runErr, context.DeadlineExceeded):
		status, summary = Failed, "The check exceeded its time limit."
	default:
		status, summary = Failed, runErr.Error()
	}
	if t.Kind == AgentTurn || t.Kind == BackgroundWork {
		switch status {
		case Succeeded:
			summary = "Replied."
			if t.Kind == BackgroundWork {
				summary = "Finished."
			}
		case Interrupted:
			summary, reply = "Interrupted.", "I was interrupted when the server stopped. Send the request again to retry."
			if action.githubWriteRun {
				reply = GitHubWriteUncertain
			}
		case Failed:
			if errors.Is(runErr, context.DeadlineExceeded) && action.githubWriteRun {
				reply = GitHubWriteUncertain
			} else if errors.Is(runErr, context.DeadlineExceeded) {
				reply = "This request took longer than the time limit. Try splitting it into smaller steps."
			} else {
				reply = "I couldn't finish this request. " + summary
			}
			summary = "Couldn't finish."
		}
	}
	// Keep the result until it commits. Retrying the transaction never
	// re-executes the task. Give shutdown one final bounded attempt.
	for {
		shuttingDown := server.Err() != nil
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if status == Interrupted && t.Kind == BackgroundWork {
			requeued, err := w.store.RetryInterruptedWatch(ctx, t.ID)
			if err != nil {
				log.Printf("requeue interrupted watch %s: %v", t.ID, err)
				cancel()
				return // Recover will attempt the same transaction on startup.
			}
			if requeued {
				cancel()
				return
			}
		}
		err := w.store.finishTurn(ctx, t.ID, status, summary, reply, action)
		cancel()
		if err == nil || errors.Is(err, ErrInactive) {
			if err == nil && status == Succeeded && len(action.managementStopped) != 0 {
				w.signalManagedStops(action.managementStopped)
			}
			if err == nil && (action.hasHandoff() && !action.managementCard || action.scheduledWork) && w.background != nil {
				w.background.Wake()
			}
			return
		}
		log.Printf("save result for task %s: %v", t.ID, err)
		if shuttingDown {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-server.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}
