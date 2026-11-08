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
