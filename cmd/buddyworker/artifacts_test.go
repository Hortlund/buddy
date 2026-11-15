package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestWorkerReturnsDeliverablesBeforeFinishing(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry recovers", true: "delivery fails"}[permanent], func(t *testing.T) {
			outbox := t.TempDir()
			if err := os.WriteFile(filepath.Join(outbox, "report.md"), []byte("# Report\n"), 0600); err != nil {
				t.Fatal(err)
			}
			uploads := 0
			results := make(chan tasks.DeviceResult, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/artifacts") {
					uploads++
					if permanent {
						http.Error(w, "rejected", http.StatusRequestEntityTooLarge)
						return
					}
					if uploads == 1 {
						http.Error(w, "temporary failure", http.StatusServiceUnavailable)
						return
					}
					var input struct {
						Lease    string             `json:"lease"`
						Artifact tasks.WorkArtifact `json:"artifact"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Lease != "lease" || string(input.Artifact.Data) != "# Report\n" {
						t.Error("retry lost its lease or file contents", input, err)
					}
					w.WriteHeader(http.StatusOK)
					return
				}
				var result tasks.DeviceResult
				if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
					t.Error(err)
				}
				results <- result
				w.WriteHeader(http.StatusOK)
			}))
			state := t.TempDir()
			if err := os.Chmod(state, 0700); err != nil {
				t.Fatal(err)
			}
			identity, err := localconfig.EnsureIdentity(state)
			if err != nil {
				t.Fatal(err)
			}
			server.TLS = &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS13}
			server.StartTLS()
			defer server.Close()
			certificate, err := localconfig.ConnectionCertificate(state)
			if err != nil {
				t.Fatal(err)
			}
			client := api.NewConnectionClient(localconfig.Connection{Server: server.URL, Certificate: certificate, Token: "fixture"})
			defer client.Close()
			job := tasks.DeviceJob{ID: "tsk_delivery", Lease: "lease", Seconds: 20}
			runJob(t.Context(), client, func(ctx context.Context, turn tasks.Turn, emit tasks.Emit) (string, error) {
				err := returnWorkArtifacts(ctx, outbox, func(ctx context.Context, file tasks.WorkArtifact) error {
					return client.UploadWorkArtifact(ctx, "device", job.ID, job.Lease, file)
				})
				return "Created the report.", err
			}, "device", job, time.Now(), t.TempDir(), true)
			var result tasks.DeviceResult
			select {
			case result = <-results:
			default:
				t.Fatal("worker did not report its outcome")
			}
			if permanent {
				if uploads != 1 || result.Status != tasks.Failed || !strings.Contains(result.Reply, "report.md") {
					t.Fatal("failed delivery was reported as success or retried as transient", uploads, result)
				}
			} else if uploads != 2 || result.Status != tasks.Succeeded {
				t.Fatal("job finished before the recovered upload", uploads, result)
			}
			if data, err := os.ReadFile(filepath.Join(outbox, "report.md")); err != nil || string(data) != "# Report\n" {
				t.Fatal("worker did not retain its deliverable", err)
			}
		})
	}
}

func TestWorkerDeliversEveryCommandBeforeFinishing(t *testing.T) {
	testWorkerCommandDelivery(t, false, 20*time.Second)
}

func TestRejectedCommandProgressDoesNotLoseWorkQuestion(t *testing.T) {
	testWorkerCommandDelivery(t, true, 20*time.Second)
}

func TestSlowCommandProgressLeavesTimeToReportBeforeJobDeadline(t *testing.T) {
	testWorkerCommandDelivery(t, false, 2*time.Second)
}

func testWorkerCommandDelivery(t *testing.T, rejected bool, timeout time.Duration) {
	t.Helper()
	claimed := time.Now()
	slow := timeout == 2*time.Second
	var attempts int
	var stored []string
	results := make(chan tasks.DeviceResult, 1)
	slowPulse := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulse") {
			var pulse tasks.DevicePulse
			if err := json.NewDecoder(r.Body).Decode(&pulse); err != nil || pulse.Lease != "lease" {
				t.Error("invalid pulse", pulse, err)
			}
			if slow {
				slowPulse <- struct{}{}
				<-r.Context().Done()
				return
			}
			attempts++
			if rejected {
				http.Error(w, "invalid progress", http.StatusBadRequest)
				return
			}
			if attempts == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			for _, run := range pulse.Commands {
				if run.Status != tasks.Succeeded || run.Stdout != run.RunID || run.ExitCode == nil || *run.ExitCode != 0 {
					t.Errorf("missing completed result: %+v", run)
				}
				stored = append(stored, run.RunID)
			}
			_ = json.NewEncoder(w).Encode(tasks.Running)
			return
		}
		if time.Since(claimed) >= timeout {
			http.Error(w, "job expired", http.StatusConflict)
			return
		}
		var result tasks.DeviceResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil || result.Lease != "lease" {
			t.Error("invalid result", result, err)
		}
		results <- result
	}))
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := localconfig.EnsureIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	certificate, err := localconfig.ConnectionCertificate(state)
	if err != nil {
		t.Fatal(err)
	}
	client := api.NewConnectionClient(localconfig.Connection{Server: server.URL, Certificate: certificate, Token: "fixture"})
	defer client.Close()
	ids := []string{"run_a", "run_b", "run_c", "run_d", "run_e"}
	job := tasks.DeviceJob{ID: "tsk_commands", Lease: "lease", Seconds: int(timeout.Seconds()), CommandProgress: true, CommandBatches: true, CanRequestInput: true}
	runJob(t.Context(), client, func(_ context.Context, _ tasks.Turn, emit tasks.Emit) (string, error) {
		for _, id := range ids {
			for _, status := range []tasks.Status{tasks.Running, tasks.Succeeded} {
				data, err := json.Marshal(map[string]any{
					"run_id": id, "status": status, "command": "test", "stdout": id, "exit_code": 0, "work_command": true,
				})
				if err != nil {
					return "", err
				}
				if err := emit("chat_code_run", string(data)); err != nil {
					return "", err
				}
			}
		}
		if rejected {
			return "Which test suite should I run next?", tasks.ErrWorkNeedsInput
		}
		return "Finished all five commands.", nil
	}, "device", job, claimed, t.TempDir(), true)
	select {
	case result := <-results:
		if slow {
			select {
			case <-slowPulse:
			default:
				t.Fatal("the test did not exercise a slow progress request")
			}
			if result.Status != tasks.Succeeded || result.Reply != "Finished all five commands." {
				t.Fatalf("lost result before deadline: %+v", result)
			}
		} else if rejected {
			if result.Status != tasks.NeedsInput || result.Reply != "Which test suite should I run next?" || attempts != 1 {
				t.Fatalf("result=%+v attempts=%d", result, attempts)
			}
		} else if result.Status != tasks.Succeeded || attempts != 3 || !slices.Equal(stored, ids) {
			t.Fatalf("result=%+v attempts=%d stored=%v", result, attempts, stored)
		}
	default:
		t.Fatal("worker did not report its result")
	}
}

func TestArtifactRetryStopsWithJobCancellation(t *testing.T) {
	outbox := t.TempDir()
	if err := os.WriteFile(filepath.Join(outbox, "report.md"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	err := returnWorkArtifacts(ctx, outbox, func(context.Context, tasks.WorkArtifact) error {
		attempts++
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatal("cancelled job kept uploading", attempts, err)
	}
}
