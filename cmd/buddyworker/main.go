package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"buddy/internal/api"
	"buddy/internal/buildinfo"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup-automation" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := tasks.RunAutomationSetup(ctx, os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "automation-proxy" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := tasks.RunAutomationProxy(ctx, os.Args[2:]); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}

	path := flag.String("connection", "", "path to the worker connection file")
	enroll := flag.String("enroll", "", "owner connection file used to create a worker credential")
	name := flag.String("name", "", "device name when enrolling")
	server := flag.String("server", "", "override the HTTPS origin after a server move")
	workspace := flag.String("workspace", "", "local folder available to assigned jobs")
	edits := flag.Bool("allow-edits", false, "allow jobs to write in the local workspace")
	configFile := flag.String("config", "", "host-local execution JSON with projects and automation providers")
	pool := flag.Bool("accept-pool", false, "claim portable jobs from the shared worker pool")
	codex := flag.String("codex", "codex", "Codex CLI executable")
	version := flag.Bool("version", false, "print the release version and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.Version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *path == "" {
		log.Fatal("Provide -connection with a path in a private directory.")
	}
	var err error
	if *enroll != "" {
		err = enrollWorker(ctx, *enroll, *path, *name)
	} else {
		err = serveWorker(ctx, *path, *server, *workspace, *codex, *edits, *pool, *configFile)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func enrollWorker(ctx context.Context, ownerPath, path, name string) error {
	connection, err := localconfig.ReadConnection(ownerPath)
	if err != nil {
		return err
	}
	if connection.DeviceID != "" {
		return errors.New("Enrollment needs an owner connection file.")
	}
	if _, err := localconfig.PrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Choose a new path for the worker connection file.")
	}
	client := api.NewConnectionClient(connection)
	defer client.Close()
	credential, err := client.EnrollDevice(ctx, name)
	if err != nil {
		return err
	}
	connection.DeviceID, connection.Token = credential.ID, credential.Token
	if err := localconfig.SaveConnection(path, connection, false); err != nil {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(err, client.RevokeDevice(rollback, credential.ID))
	}
	log.Printf("Worker connection saved for %s.", credential.Name)
	return nil
}

func serveWorker(ctx context.Context, path, server, workspace, codex string, edits, pool bool, configFile string) error {
	connection, err := localconfig.ReadConnection(path)
	if err != nil {
		return err
	}
	if connection.DeviceID == "" || workspace == "" {
		return errors.New("Provide a worker connection file and an explicit -workspace directory.")
	}
	if server != "" {
		connection.Server = server
		if err := connection.Validate(); err != nil {
			return err
		}
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return errors.New("Choose an existing workspace directory.")
	}
	dir, err := localconfig.PrivateDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	if overlap, err := tasks.WorkspacePathsOverlap(workspace, dir); err != nil || overlap {
		return errors.New("Choose a workspace separate from the worker credential directory.")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	source := ""
	if _, err := os.Stat("cmd/buddyd/main.go"); err == nil {
		source = "."
	}
	runtime, err := tasks.NewCodexRuntime(codex, source, localconfig.CodexKey{Dir: dir}, dir, exe)
	if err != nil {
		return err
	}
	if configFile == "" {
		configFile = filepath.Join(dir, "execution.json")
	}
	cfg, err := tasks.LoadExecutionConfig(configFile)
	if err != nil {
		return err
	}
	if err = runtime.LoadHostExecution(ctx, cfg); err != nil {
		return err
	}
	if pool {
		_, source, err := runtime.Key.Read()
		if err != nil {
			return err
		}
		switch runtime.RuntimeStatus(ctx, source) {
		case "signed_in", "configured":
		default:
			return errors.New("Install Codex and sign in before joining the worker pool.")
		}
	}
	client := api.NewConnectionClient(connection)
	defer client.Close()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	if pool {
		log.Print("Buddy worker is polling for assigned and shared pool jobs.")
	} else {
		log.Print("Buddy worker is polling for assigned jobs.")
	}
	var nextProfile time.Time
	for ctx.Err() == nil {
		start := time.Now()
		if time.Now().After(nextProfile) {
			nextProfile = time.Now().Add(30 * time.Second)
			if err := client.ReportHostProfile(ctx, connection.DeviceID, runtime.DiscoverHost(ctx)); err != nil {
				log.Printf("host report: %v", err)
			} else {
				nextProfile = time.Now().Add(time.Minute)
			}
		}
		job, err := client.ClaimDeviceWork(ctx, connection.DeviceID, pool, edits)
		switch {
		case unauthorized(err):
			return errors.New("The server rejected the worker credential.")
		case err == nil && job != nil:
			if job.NeedsStart {
				start = time.Now()
				if err := acknowledgeJob(ctx, client, connection.DeviceID, job, start); err != nil {
					if unauthorized(err) {
						return errors.New("The server rejected the worker credential.")
					}
					continue
				}
			}
			run := func(jobCtx context.Context, turn tasks.Turn, emit tasks.Emit) (string, error) {
				turn.ExecutionPlan = job.ExecutionPlan
				if job.ModelSelection {
					turn.RecordModelSelection = func(ctx context.Context, choice tasks.WorkerModelSelection) error {
						return client.ReportWorkerModel(ctx, connection.DeviceID, job.ID, job.Lease, choice)
					}
				} else {
					// Older homes cannot save a decision for retry. Keep their supplied settings.
					turn.Agent.ModelPreferences = turn.Agent.ModelPreferences.Clone()
					if turn.Agent.ModelPreferences == nil {
						turn.Agent.ModelPreferences = &tasks.ModelPreferences{}
					}
					turn.Agent.ModelPreferences.Mode = "fixed"
				}
				if job.ControlToken != "" {
					turn.ControlForward = func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
						return client.ForwardWorkControl(ctx, job.ID, job.ControlToken, raw)
					}
				}
				if job.ProjectID != "" {
					found := false
					for _, binding := range runtime.ExecutionConfig().Projects {
						if binding.ProjectID == job.ProjectID && binding.Path == job.Workspace {
							turn.Agent.Workspace = binding.Path
							found = true
							break
						}
					}
					if !found {
						return "", errors.New("The assigned project has no matching checkout in this worker's local configuration.")
					}
				}
				dir, branch, err := tasks.PrepareProjectWorkspace(jobCtx, turn.Agent.Workspace, job.ID, job.ProjectID, turn.Agent.AllowEdits, turn.WorkspaceMode)
				if err != nil {
					return "", err
				}
				turn.Agent.Workspace = dir
				if err = client.ReportWorkerSession(jobCtx, connection.DeviceID, job.ID, job.Lease, dir, branch); err != nil {
					return "", err
				}
				if turn.Agent.AllowEdits {
					turn.OutputDirectory, err = tasks.ArtifactOutbox(dir, job.ID)
					if err != nil {
						return "", err
					}
				}
				monitorCtx, stopMonitor := context.WithCancel(jobCtx)
				defer stopMonitor()
				automation := slices.Contains(turn.Requirements, "desktop") || slices.Contains(turn.Requirements, "browser")
				if automation {
					turn.AutomationAnswers, turn.AutomationRequest = job.AutomationAnswers, job.AutomationRequest
					turn.AutomationControlLookup = func(ctx context.Context) (tasks.AutomationControl, error) {
						return client.AutomationControlState(ctx, connection.DeviceID, job.ID, job.Lease)
					}
					go func() {
						ticker := time.NewTicker(2 * time.Second)
						defer ticker.Stop()
						var last time.Time
						for {
							select {
							case <-monitorCtx.Done():
								return
							case <-ticker.C:
								control, err := turn.AutomationControlLookup(monitorCtx)
								if err == nil {
									_ = runtime.ApplyAutomationControl(job.ID, control)
								}
								frame, err := runtime.ExecutionFrame(job.ID)
								if err == nil && len(frame.Data) > 0 && frame.At.After(last) {
									if client.UploadExecutionFrame(monitorCtx, connection.DeviceID, job.ID, job.Lease, frame) == nil {
										last = frame.At
									}
								}
							}
						}
					}()
				}
				reply, err := runtime.Run(jobCtx, turn, emit)
				if automation {
					if frame, frameErr := runtime.ExecutionFrame(job.ID); frameErr == nil && len(frame.Data) > 0 {
						_ = client.UploadExecutionFrame(jobCtx, connection.DeviceID, job.ID, job.Lease, frame)
					}
				}
				if err == nil && turn.OutputDirectory != "" {
					err = returnWorkArtifacts(jobCtx, turn.OutputDirectory, func(ctx context.Context, artifact tasks.WorkArtifact) error {
						return client.UploadWorkArtifact(ctx, connection.DeviceID, job.ID, job.Lease, artifact)
					})
				}
				return reply, err
			}
			runJob(ctx, client, run, connection.DeviceID, *job, start, workspace, edits)
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return nil
}

func acknowledgeJob(ctx context.Context, client *api.Client, device string, job *tasks.DeviceJob, start time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, start.Add(12*time.Second))
	defer cancel()
	for {
		err := client.StartDeviceJob(ctx, device, job.ID, job.Lease)
		if err == nil || unauthorized(err) || inactive(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

type jobOutcome struct {
	reply   string
	err     error
	command *tasks.ChatCodeRun
}

// Coalesce updates to the same command while retaining each result until the
// server acknowledges it. The runner can publish during a heartbeat.
type pendingCommands struct {
	mu    sync.Mutex
	order []string
	runs  map[string]*tasks.ChatCodeRun
}

func (p *pendingCommands) record(run *tasks.ChatCodeRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs[run.RunID] == nil {
		p.order = append(p.order, run.RunID)
	}
	p.runs[run.RunID] = run
}

func (p *pendingCommands) batch() []*tasks.ChatCodeRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	var batch []*tasks.ChatCodeRun
	for _, id := range p.order[:min(len(p.order), tasks.DeviceCommandBatchLimit)] {
		batch = append(batch, p.runs[id])
	}
	return batch
}

func (p *pendingCommands) acknowledge(batch []*tasks.ChatCodeRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, run := range batch {
		if p.runs[run.RunID] == run {
			delete(p.runs, run.RunID)
		}
	}
	p.order = slices.DeleteFunc(p.order, func(id string) bool { return p.runs[id] == nil })
}

func runJob(ctx context.Context, client *api.Client, run tasks.AgentRunner, device string, job tasks.DeviceJob,
	claimed time.Time, workspace string, edits bool) {
	jobDeadline := claimed.Add(time.Duration(job.Seconds) * time.Second)
	jobCtx, cancel := context.WithDeadline(ctx, jobDeadline)
	defer cancel()
	leaseSafety := 18 * time.Second
	leaseUntil := claimed.Add(min(leaseSafety, jobDeadline.Sub(claimed)))
	guard := time.AfterFunc(time.Until(leaseUntil), cancel)
	defer guard.Stop()
	job.Agent.Workspace = workspace
	job.Agent.AllowEdits = job.Agent.AllowEdits && edits
	turn := tasks.Turn{Agent: job.Agent, Language: job.Language, Title: job.Title, Brief: job.Brief, Personality: job.Personality,
		Attachments: job.Attachments, DeviceExecution: true, CanRequestInput: job.CanRequestInput, Skill: job.Skill, BuddyTeam: job.BuddyTeam}
	progress := make(chan tasks.DevicePulse, 1)
	done := make(chan jobOutcome, 1)
	commands := pendingCommands{runs: make(map[string]*tasks.ChatCodeRun)}
	go func() {
		var pulse tasks.DevicePulse
		reply, err := run(jobCtx, turn, func(stream, value string) error {
			switch stream {
			case "activity", "progress":
				pulse.Kind = tasks.DeviceActivity(value)
			case "commentary", "work_update":
				pulse.Update = value
				pulse.CompletedUpdate = job.CanRequestInput && stream == "work_update"
			case "chat_code_run":
				var command tasks.ChatCodeRun
				if job.CommandProgress && json.Unmarshal([]byte(value), &command) == nil && command.WorkCommand {
					if job.CommandBatches && command.RunID != "" {
						commands.record(&command)
					} else {
						pulse.Command = &command
					}
				}
			}
			select {
			case <-progress:
			default:
			}
			progress <- pulse
			return nil
		})
		done <- jobOutcome{reply, err, pulse.Command}
	}()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var pulse tasks.DevicePulse
	var outcome jobOutcome
	sendPulse := func(ctx context.Context, pulse tasks.DevicePulse) (tasks.Status, error) {
		pulse.Lease = job.Lease
		pulse.Commands = commands.batch()
		if len(pulse.Commands) != 0 {
			pulse.Command = nil
		}
		status, err := client.PulseDeviceJob(ctx, device, job.ID, pulse)
		if err == nil && (status == tasks.Running || status == tasks.Cancelling) {
			commands.acknowledge(pulse.Commands)
		}
		return status, err
	}
loop:
	for {
		select {
		case outcome = <-done:
			break loop
		case pulse = <-progress:
		case <-ticker.C:
			start := time.Now()
			pulseUntil := leaseUntil.Add(-2 * time.Second)
			if !start.Before(pulseUntil) {
				continue
			}
			pulseCtx, stopPulse := context.WithDeadline(jobCtx, pulseUntil)
			status, err := sendPulse(pulseCtx, pulse)
			stopPulse()
			if err == nil && status == tasks.Running {
				leaseUntil = start.Add(min(leaseSafety, jobDeadline.Sub(start)))
				guard.Reset(time.Until(leaseUntil))
			} else if err == nil || unauthorized(err) || inactive(err) {
				cancel()
			}
		case <-jobCtx.Done():
			outcome = <-done
			break loop
		}
	}
	report := tasks.DeviceResult{Lease: job.Lease, Status: tasks.Succeeded, Reply: outcome.reply, Command: outcome.command}
	if errors.Is(outcome.err, tasks.ErrWorkNeedsInput) && job.CanRequestInput {
		report.Status = tasks.NeedsInput
	} else if outcome.err != nil {
		report.Status, report.Reply = tasks.Failed, "The device couldn't finish. "+outcome.err.Error()
		if errors.Is(outcome.err, context.Canceled) {
			report.Status, report.Reply = tasks.Interrupted, "The device stopped before this work finished."
		}
	}
	reportCtx, stop := context.WithDeadline(context.WithoutCancel(ctx), leaseUntil)
	defer stop()
	// Give pending progress a short chance to arrive while reserving time for
	// the reply or question, which must reach the server independently.
	if len(commands.batch()) != 0 {
		remaining := time.Until(leaseUntil)
		progressCtx, stopProgress := context.WithTimeout(reportCtx, min(4*time.Second, remaining/2, remaining-time.Second))
		for progressCtx.Err() == nil && len(commands.batch()) != 0 {
			status, err := sendPulse(progressCtx, tasks.DevicePulse{})
			if err == nil && (status == tasks.Running || status == tasks.Cancelling) {
				continue
			}
			if err == nil || unauthorized(err) || inactive(err) || hasHTTPStatus(err, http.StatusBadRequest) {
				break
			}
			select {
			case <-progressCtx.Done():
			case <-time.After(time.Second):
			}
		}
		stopProgress()
	}
	for reportCtx.Err() == nil {
		err := client.FinishDeviceJob(reportCtx, device, job.ID, report)
		if err == nil || unauthorized(err) || inactive(err) {
			return
		}
		select {
		case <-reportCtx.Done():
		case <-time.After(time.Second):
		}
	}
	log.Printf("Could not deliver the result for %s before its lease ended.", job.ID)
}

func unauthorized(err error) bool { return hasHTTPStatus(err, http.StatusUnauthorized) }
func inactive(err error) bool {
	return hasHTTPStatus(err, http.StatusNotFound) || hasHTTPStatus(err, http.StatusConflict)
}
func hasHTTPStatus(err error, code int) bool {
	var remote *api.HTTPError
	return errors.As(err, &remote) && remote.Status == code
}
