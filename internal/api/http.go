// Package api is the versioned contract shared by the server and its clients.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"buddy/internal/automations"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

type errorBody struct {
	Error string `json:"error"`
}

const ConnectionChatProtocol = 37
const WatchDraftProtocol = 38
const MCPProtocol = 40
const MobileProtocol = 42
const DesktopProtocol = 55
const AttachmentProtocol = 11
const attachmentTransferTimeout = 2 * time.Minute

func allowAttachmentTransfer(w http.ResponseWriter, upload bool) {
	deadline := time.Now().Add(attachmentTransferTimeout)
	controller := http.NewResponseController(w)
	if upload {
		_ = controller.SetReadDeadline(deadline)
	}
	_ = controller.SetWriteDeadline(deadline)
}

func Handler(token string, store *tasks.Store, worker *tasks.Worker, codex *tasks.CodexRuntime, services ...*automations.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]any{"service": "buddy", "status": "ok", "desktop_protocol": DesktopProtocol})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ready(ctx); err != nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"service": "buddy", "status": "unavailable"})
			return
		}
		write(w, http.StatusOK, map[string]string{"service": "buddy", "status": "ready"})
	})
	var voiceInUse atomic.Bool
	workspaceRoutes(mux, store, &voiceInUse)
	historyRoutes(mux, store, &voiceInUse)
	budMemoryRoutes(mux, store)
	preferencesRoutes(mux, store, worker)
	var integrations *automations.Service
	if len(services) != 0 && services[0] != nil {
		integrations = services[0]
		integrationRoutes(mux, store, integrations)
	}
	standingRoutes(mux, store, integrations)
	skillRoutes(mux, store)
	devices := deviceRoutes(mux, store)
	if codex != nil {
		codexRoutes(mux, codex)
		mcpRoutes(mux, store, codex)
		workspaceFileRoutes(mux, store, codex)
		voiceRoutes(mux, store, worker, localconfig.VoiceKey{Dir: codex.Key.Dir}, dialRealtime, &voiceInUse)
	}
	mux.HandleFunc("GET /v1/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		var before int64
		if cursor := r.URL.Query().Get("before"); cursor != "" {
			var err error
			before, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || before <= 0 {
				result(w, 0, nil, &tasks.InputError{Message: "Invalid conversation cursor."})
				return
			}
		}
		reactionsAfter := int64(-1)
		if cursor := r.URL.Query().Get("reactions_after"); cursor != "" {
			var err error
			reactionsAfter, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || reactionsAfter < 0 {
				result(w, 0, nil, &tasks.InputError{Message: "Invalid reaction cursor."})
				return
			}
		}
		conversation, err := store.ConversationWithReactions(r.Context(), r.PathValue("id"), before, reactionsAfter)
		if err == nil {
			if conversation.Active != nil && conversation.Active.Status == tasks.Running {
				conversation.Active.CanSteer = worker.CanSteer(conversation.Active.ID)
			}
			for i, item := range conversation.Work {
				conversation.Work[i].CanSteer = item.Status == tasks.Running && worker.CanSteer(item.ID)
			}
		}
		result(w, http.StatusOK, conversation, err)
	})
	mux.HandleFunc("PUT /v1/conversations/{id}/messages/{message}/reaction", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("message"), 10, 64)
		if err != nil || id <= 0 {
			result(w, 0, nil, tasks.ErrNotFound)
			return
		}
		var input struct {
			Emoji *string `json:"emoji"`
		}
		if err := decode(w, r, &input, 256); err != nil || input.Emoji == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Choose a reaction or remove your current one."})
			return
		}
		update, err := store.SetReaction(r.Context(), r.PathValue("id"), id, *input.Emoji)
		result(w, http.StatusOK, update, err)
	})
	mux.HandleFunc("GET /v1/tasks/{task}/attachments/{position}", func(w http.ResponseWriter, r *http.Request) {
		allowAttachmentTransfer(w, false)
		position, err := strconv.Atoi(r.PathValue("position"))
		if err != nil || position < 0 || position >= tasks.MaxAttachments {
			result(w, 0, nil, tasks.ErrNotFound)
			return
		}
		file, err := store.Attachment(r.Context(), r.PathValue("task"), position)
		result(w, http.StatusOK, file, err)
	})
	mux.HandleFunc("GET /v1/tasks/{task}/generated-image", func(w http.ResponseWriter, r *http.Request) {
		allowAttachmentTransfer(w, false)
		file, err := store.GeneratedImage(r.Context(), r.PathValue("task"))
		w.Header().Set("Cache-Control", "no-store")
		result(w, http.StatusOK, file, err)
	})
	mux.HandleFunc("POST /v1/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 || r.ContentLength > 80<<10 {
			allowAttachmentTransfer(w, true)
		}
		var input tasks.MessageRequest
		if err := decode(w, r, &input, 12<<20); err != nil {
			result(w, 0, nil, err)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 128 {
			result(w, 0, nil, &tasks.InputError{Message: "Invalid request ID."})
			return
		}
		t, created, err := store.SendMessage(r.Context(), r.PathValue("id"), key, input)
		status := http.StatusOK
		if err == nil && created {
			status = http.StatusAccepted
			w.Header().Set("Location", "/v1/tasks/"+t.ID)
			worker.Wake()
		}
		result(w, status, t, err)
	})
	mux.HandleFunc("POST /v1/conversations/{id}/cards/{card}/answer", func(w http.ResponseWriter, r *http.Request) {
		var input cardAnswer
		if err := decode(w, r, &input, 1024); err != nil || input.Choice == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Choose an answer on the card."})
			return
		}
		t, created, err := store.AnswerCard(r.Context(), r.PathValue("id"), r.PathValue("card"), *input.Choice)
		if created && err == nil {
			worker.Wake()
		}
		result(w, http.StatusOK, t, err)
	})
	mux.HandleFunc("GET /v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, err := store.Agent(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, a, err)
	})
	mux.HandleFunc("PUT /v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.Agent
		if err := decode(w, r, &input, 256<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		a, err := store.SaveAgent(r.Context(), r.PathValue("id"), input)
		result(w, http.StatusOK, a, err)
	})

	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		items, err := store.List(r.Context())
		result(w, http.StatusOK, items, err)
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.Request
		if err := decode(w, r, &input, 32<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 128 {
			result(w, 0, nil, &tasks.InputError{Message: "Invalid request ID."})
			return
		}
		req, err := tasks.Prepare(r.Context(), input)
		if err != nil {
			result(w, 0, nil, err)
			return
		}
		t, created, err := store.Create(r.Context(), req, key)
		status := http.StatusOK
		if err == nil && created {
			status = http.StatusAccepted
			w.Header().Set("Location", "/v1/tasks/"+t.ID)
			worker.Wake()
		}
		result(w, status, t, err)
	})
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, err := store.Get(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, t, err)
	})
	mux.HandleFunc("GET /v1/tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var after int64
		if raw := r.URL.Query().Get("after"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 {
				result(w, 0, nil, &tasks.InputError{Message: "Invalid event cursor."})
				return
			}
			after = parsed
		}
		id := r.PathValue("id")
		if _, err := store.Get(r.Context(), id); err != nil {
			result(w, 0, nil, err)
			return
		}
		events, err := store.Events(r.Context(), id, after)
		result(w, http.StatusOK, events, err)
	})
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		t, err := worker.Cancel(r.Context(), r.PathValue("id"), r.URL.Query().Get("queued") == "true")
		result(w, http.StatusOK, t, err)
	})
	mux.HandleFunc("POST /v1/tasks/{id}/steer", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Text string `json:"text"`
		}
		if err := decode(w, r, &input, 80<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 128 {
			result(w, 0, nil, &tasks.InputError{Message: "Invalid request ID."})
			return
		}
		message, err := worker.Steer(r.Context(), r.PathValue("id"), input.Text, key)
		result(w, http.StatusAccepted, message, err)
	})
	var pushTopic string
	if integrations != nil {
		pushTopic = integrations.MobilePushTopic()
	}
	mobile := mobileRoutes(mux, store, pushTopic)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/hooks/github" && len(services) != 0 && services[0] != nil {
			services[0].GitHubWebhook(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/worker/") {
			devices.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.EscapedPath() == mobileRedeem {
			redeemMobilePairing(w, r, store)
			return
		}
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(value), []byte(token)) != 1 {
				if ok && strings.HasPrefix(value, "mobile_") {
					id, err := store.MobileClientID(r.Context(), value)
					if err == nil {
						if _, pattern := mobile.Handler(r); pattern == "" {
							write(w, http.StatusForbidden, errorBody{"This action requires access from a Buddy desktop."})
							return
						}
						mobile.ServeHTTP(w, r.WithContext(tasks.WithMobileClient(r.Context(), id)))
						return
					}
					if !errors.Is(err, tasks.ErrNotFound) {
						result(w, 0, nil, err)
						return
					}
				}
				w.Header().Set("WWW-Authenticate", "Bearer")
				write(w, http.StatusUnauthorized, errorBody{"Invalid or missing Buddy server token."})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return &tasks.InputError{Message: "Invalid request."}
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &tasks.InputError{Message: "Expected a single JSON request."}
	}
	return nil
}

func result(w http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		write(w, status, value)
		return
	}
	var input *tasks.InputError
	switch {
	case errors.As(err, &input):
		write(w, http.StatusBadRequest, errorBody{input.Error()})
	case errors.Is(err, tasks.ErrMobileRevoked):
		w.Header().Set("WWW-Authenticate", "Bearer")
		write(w, http.StatusUnauthorized, errorBody{err.Error()})
	case errors.Is(err, tasks.ErrNotFound):
		write(w, http.StatusNotFound, errorBody{err.Error()})
	case errors.Is(err, tasks.ErrConflict), errors.Is(err, tasks.ErrInactive),
		errors.Is(err, tasks.ErrSteeringUnavailable), errors.Is(err, tasks.ErrNoLongerQueued),
		errors.Is(err, tasks.ErrWorkspaceFileChanged), errors.Is(err, tasks.ErrWorkspaceFolderChanged):
		write(w, http.StatusConflict, errorBody{err.Error()})
	case errors.Is(err, tasks.ErrQueueFull):
		write(w, http.StatusTooManyRequests, errorBody{err.Error()})
	default:
		log.Printf("API: %v", err)
		write(w, http.StatusInternalServerError, errorBody{"Buddy couldn't read or save the task."})
	}
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
