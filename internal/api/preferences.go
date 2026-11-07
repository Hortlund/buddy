package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"buddy/internal/tasks"
)

const WorkbenchProtocol = 22
const WorkHistoryProtocol = 25

func preferencesRoutes(mux *http.ServeMux, store *tasks.Store, worker *tasks.Worker) {
	mux.HandleFunc("GET /v1/preferences/languages", func(w http.ResponseWriter, r *http.Request) {
		prefs, err := store.Languages(r.Context())
		result(w, http.StatusOK, prefs, err)
	})
	mux.HandleFunc("PUT /v1/preferences/languages", func(w http.ResponseWriter, r *http.Request) {
		var prefs tasks.LanguagePreferences
		if err := decode(w, r, &prefs, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		saved, err := store.SaveLanguages(r.Context(), prefs)
		result(w, http.StatusOK, saved, err)
	})
	mux.HandleFunc("GET /v1/work", func(w http.ResponseWriter, r *http.Request) {
		work, err := store.WorkOverview(r.Context(), r.URL.Query().Get("chat"))
		result(w, http.StatusOK, work, err)
	})
	mux.HandleFunc("GET /v1/work/history", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		page, err := store.WorkHistory(r.Context(), query.Get("chat"), query.Get("before"))
		result(w, http.StatusOK, page, err)
	})
	mux.HandleFunc("GET /v1/work/{job}", func(w http.ResponseWriter, r *http.Request) {
		detail, err := store.ReferencedWorkDetails(r.Context(), r.PathValue("job"))
		result(w, http.StatusOK, detail, err)
	})
	mux.HandleFunc("GET /v1/conversations/{chat}/work/{job}", func(w http.ResponseWriter, r *http.Request) {
		detail, err := store.WorkDetails(r.Context(), r.PathValue("chat"), r.PathValue("job"))
		result(w, http.StatusOK, detail, err)
	})
	mux.HandleFunc("POST /v1/conversations/{chat}/work-inputs/{input}/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 || r.ContentLength > 80<<10 {
			allowAttachmentTransfer(w, true)
		}
		var input workInputAnswer
		if err := decode(w, r, &input, 12<<20); err != nil {
			result(w, 0, nil, err)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 128 {
			result(w, 0, nil, &tasks.InputError{Message: "Invalid request ID."})
			return
		}
		resumed, err := store.AnswerWorkInput(r.Context(), r.PathValue("chat"), r.PathValue("input"), input.Text, key, input.Attachments)
		if resumed && err == nil {
			worker.WakeBackground()
		}
		result(w, http.StatusOK, nil, err)
	})
}

type workInputAnswer struct {
	Text        string             `json:"text"`
	Attachments []tasks.Attachment `json:"attachments,omitempty"`
}

func (c *Client) AnswerWorkInput(ctx context.Context, chat, input, text, key string, files []tasks.Attachment) error {
	timeout := 8 * time.Second
	if len(files) != 0 {
		timeout = attachmentTransferTimeout
	}
	path := "/v1/conversations/" + url.PathEscape(chat) + "/work-inputs/" + url.PathEscape(input) + "/answer"
	return c.callWithTimeout(ctx, timeout, http.MethodPost, path, key, workInputAnswer{text, files}, nil)
}

func (c *Client) Languages(ctx context.Context) (tasks.LanguagePreferences, error) {
	var prefs tasks.LanguagePreferences
	err := c.call(ctx, http.MethodGet, "/v1/preferences/languages", "", nil, &prefs)
	return prefs, err
}

func (c *Client) SaveLanguages(ctx context.Context, prefs tasks.LanguagePreferences) (tasks.LanguagePreferences, error) {
	var saved tasks.LanguagePreferences
	err := c.call(ctx, http.MethodPut, "/v1/preferences/languages", "", prefs, &saved)
	return saved, err
}

func (c *Client) WorkOverview(ctx context.Context, chat string) ([]tasks.WorkItem, error) {
	var work []tasks.WorkItem
	path := "/v1/work"
	if chat != "" {
		path += "?chat=" + url.QueryEscape(chat)
	}
	err := c.call(ctx, http.MethodGet, path, "", nil, &work)
	return work, err
}

func (c *Client) WorkDetails(ctx context.Context, chat, job string) (tasks.WorkDetails, error) {
	var detail tasks.WorkDetails
	path := "/v1/conversations/" + url.PathEscape(chat) + "/work/" + url.PathEscape(job)
	err := c.call(ctx, http.MethodGet, path, "", nil, &detail)
	return detail, err
}

func (c *Client) ReferencedWorkDetails(ctx context.Context, job string) (tasks.WorkDetails, error) {
	var detail tasks.WorkDetails
	err := c.call(ctx, http.MethodGet, "/v1/work/"+url.PathEscape(job), "", nil, &detail)
	return detail, err
}

func (c *Client) WorkHistory(ctx context.Context, chat, before string) (tasks.WorkHistoryPage, error) {
	var page tasks.WorkHistoryPage
	query := url.Values{"chat": {chat}, "before": {before}}
	err := c.call(ctx, http.MethodGet, "/v1/work/history?"+query.Encode(), "", nil, &page)
	return page, err
}
