package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func deviceRoutes(owner *http.ServeMux, store *tasks.Store) http.Handler {
	owner.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		devices, err := store.Devices(r.Context())
		result(w, http.StatusOK, devices, err)
	})
	owner.HandleFunc("POST /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		var input deviceEnrollment
		if err := decode(w, r, &input, 2048); err != nil {
			result(w, 0, nil, err)
			return
		}
		credential, err := store.EnrollDevice(r.Context(), input.Name)
		result(w, http.StatusCreated, credential, err)
	})
	owner.HandleFunc("DELETE /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.RevokeDevice(r.Context(), r.PathValue("id")))
	})

	worker := http.NewServeMux()
	requireDevice := func(f func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("device")
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !store.AuthenticateDevice(r.Context(), id, token) {
				write(w, http.StatusUnauthorized, errorBody{"Invalid or revoked worker credential."})
				return
			}
			f(w, r, id)
		}
	}
	worker.HandleFunc("POST /v1/worker/{device}/claim", requireDevice(func(w http.ResponseWriter, r *http.Request, id string) {
		if r.URL.Query().Get("delivery") == "1" {
			allowAttachmentTransfer(w, false)
			pool, edits := r.URL.Query().Get("pool") == "1", r.URL.Query().Get("edits") == "1"
			claim := store.ReserveDeviceWork
			if r.URL.Query().Get("skills") == "1" {
				claim = store.ReserveDeviceSkillWork
			}
			job, err := claim(r.Context(), id, pool, edits)
			result(w, http.StatusOK, job, err)
			return
		}
		// Older workers can still take regular jobs. Attachment jobs require
		// acknowledgment so a lost download never consumes a job.
		job, err := store.ClaimDeviceJob(r.Context(), id)
		result(w, http.StatusOK, job, err)
	}))
	worker.HandleFunc("POST /v1/worker/{device}/jobs/{job}/start", requireDevice(func(w http.ResponseWriter, r *http.Request, id string) {
		var input deviceStart
		if err := decode(w, r, &input, 2048); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.StartDeviceJob(r.Context(), id, r.PathValue("job"), input.Lease))
	}))
	worker.HandleFunc("POST /v1/worker/{device}/jobs/{job}/pulse", requireDevice(func(w http.ResponseWriter, r *http.Request, id string) {
		var pulse tasks.DevicePulse
		if err := decode(w, r, &pulse, 8<<10); err != nil || len(pulse.Update) > 900+len("…") {
			result(w, 0, nil, &tasks.InputError{Message: "Invalid device progress."})
			return
		}
		status, err := store.PulseDeviceJob(r.Context(), id, r.PathValue("job"), pulse)
		result(w, http.StatusOK, status, err)
	}))
	worker.HandleFunc("POST /v1/worker/{device}/jobs/{job}/finish", requireDevice(func(w http.ResponseWriter, r *http.Request, id string) {
		var report tasks.DeviceResult
		if err := decode(w, r, &report, 320<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.FinishDeviceJob(r.Context(), id, r.PathValue("job"), report))
	}))
	return worker
}

type deviceEnrollment struct {
	Name string `json:"name"`
}

type deviceStart struct {
	Lease string `json:"lease"`
}

func (c *Client) Devices(ctx context.Context) ([]tasks.Device, error) {
	var devices []tasks.Device
	err := c.call(ctx, http.MethodGet, "/v1/devices", "", nil, &devices)
	return devices, err
}

func (c *Client) EnrollDevice(ctx context.Context, name string) (tasks.DeviceCredential, error) {
	var credential tasks.DeviceCredential
	err := c.call(ctx, http.MethodPost, "/v1/devices", "", deviceEnrollment{Name: name}, &credential)
	return credential, err
}

func (c *Client) PairDevice(ctx context.Context, name, server, path string) error {
	_, err := c.CreateWorkerSetup(ctx, name, server, path)
	return err
}

func (c *Client) CreateWorkerSetup(ctx context.Context, name, server, path string) (string, error) {
	if err := localconfig.ValidateServerURL(server); err != nil {
		return "", err
	}
	cert, err := c.certificate()
	if err != nil {
		return "", err
	}
	worker, err := c.EnrollDevice(ctx, name)
	if err != nil {
		return "", err
	}
	err = localconfig.SavePortableConnection(path, localconfig.Connection{
		Server: server, Certificate: cert, DeviceID: worker.ID, Token: worker.Token,
	})
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return "", errors.Join(err, c.RevokeDevice(cleanup, worker.ID))
	}
	return worker.ID, nil
}

func (c *Client) RevokeDevice(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/devices/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) ClaimDeviceJob(ctx context.Context, id string) (*tasks.DeviceJob, error) {
	return c.ClaimDeviceWork(ctx, id, false, false)
}

func (c *Client) ClaimDeviceWork(ctx context.Context, id string, pool, edits bool) (*tasks.DeviceJob, error) {
	var job *tasks.DeviceJob
	path := "/v1/worker/" + url.PathEscape(id) + "/claim?attachments=1&delivery=1&skills=1"
	if pool {
		path += "&pool=1"
		if edits {
			path += "&edits=1"
		}
	}
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodPost, path, "", nil, &job)
	return job, err
}

func (c *Client) StartDeviceJob(ctx context.Context, id, job, lease string) error {
	path := "/v1/worker/" + url.PathEscape(id) + "/jobs/" + url.PathEscape(job) + "/start"
	return c.call(ctx, http.MethodPost, path, "", deviceStart{Lease: lease}, nil)
}

func (c *Client) PulseDeviceJob(ctx context.Context, id, job string, pulse tasks.DevicePulse) (tasks.Status, error) {
	var status tasks.Status
	path := "/v1/worker/" + url.PathEscape(id) + "/jobs/" + url.PathEscape(job) + "/pulse"
	err := c.call(ctx, http.MethodPost, path, "", pulse, &status)
	return status, err
}

func (c *Client) FinishDeviceJob(ctx context.Context, id, job string, report tasks.DeviceResult) error {
	path := "/v1/worker/" + url.PathEscape(id) + "/jobs/" + url.PathEscape(job) + "/finish"
	return c.call(ctx, http.MethodPost, path, "", report, nil)
}
