package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

type Client struct {
	base        string
	token       func() (string, error)
	certificate func() (string, error)
	http        *http.Client
}

func NewClient(base, tokenFile string) *Client {
	c := newClient(base, func() (string, error) {
		dir, err := localconfig.PrivateDir(filepath.Dir(tokenFile))
		if err != nil {
			return "", err
		}
		return localconfig.ReadToken(filepath.Join(dir, filepath.Base(tokenFile)))
	}, func() (*tls.Config, error) {
		dir, err := localconfig.PrivateDir(filepath.Dir(tokenFile))
		if err != nil {
			return nil, err
		}
		return localconfig.ClientTLS(dir)
	})
	c.certificate = func() (string, error) { return localconfig.ConnectionCertificate(filepath.Dir(tokenFile)) }
	return c
}

func NewConnectionClient(connection localconfig.Connection) *Client {
	c := newClient(connection.Server, func() (string, error) {
		return connection.Token, nil
	}, func() (*tls.Config, error) {
		return localconfig.PinnedTLS([]byte(connection.Certificate))
	})
	c.certificate = func() (string, error) { return connection.Certificate, nil }
	return c
}

func newClient(base string, token func() (string, error), tlsConfig func() (*tls.Config, error)) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		config, err := tlsConfig()
		if err != nil {
			return nil, err
		}
		dialer := tls.Dialer{Config: config}
		return dialer.DialContext(ctx, network, address)
	}
	return &Client{
		base: strings.TrimRight(base, "/"), token: token,
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// OwnerProfile retains this home's identity and access when its origin changes.
func (c *Client) OwnerProfile(server string) (localconfig.Connection, error) {
	token, err := c.token()
	if err != nil {
		return localconfig.Connection{}, err
	}
	cert, err := c.certificate()
	if err != nil {
		return localconfig.Connection{}, err
	}
	profile := localconfig.Connection{Server: server, Token: token, Certificate: cert}
	return profile, profile.Validate()
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) DesktopProtocol(ctx context.Context) (int, error) {
	var health struct {
		Service  string `json:"service"`
		Protocol int    `json:"desktop_protocol"`
	}
	if err := c.call(ctx, http.MethodGet, "/healthz", "", nil, &health); err != nil {
		return 0, err
	}
	if health.Service != "buddy" {
		return 0, fmt.Errorf("the endpoint is not a Buddy server")
	}
	return health.Protocol, nil
}

func (c *Client) Conversation(ctx context.Context, id string, before int64) (tasks.Conversation, error) {
	return c.ConversationWithReactions(ctx, id, before, -1)
}

func (c *Client) ConversationWithReactions(ctx context.Context, id string, before, reactionsAfter int64) (tasks.Conversation, error) {
	var out tasks.Conversation
	path := "/v1/conversations/" + url.PathEscape(id)
	query := url.Values{}
	if before != 0 {
		query.Set("before", strconv.FormatInt(before, 10))
	}
	if reactionsAfter >= 0 {
		query.Set("reactions_after", strconv.FormatInt(reactionsAfter, 10))
	}
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	err := c.call(ctx, http.MethodGet, path, "", nil, &out)
	return out, err
}

func (c *Client) SetReaction(ctx context.Context, chat string, message int64, emoji string) (tasks.ReactionUpdate, error) {
	var update tasks.ReactionUpdate
	path := "/v1/conversations/" + url.PathEscape(chat) + "/messages/" + strconv.FormatInt(message, 10) + "/reaction"
	err := c.call(ctx, http.MethodPut, path, "", map[string]string{"emoji": emoji}, &update)
	return update, err
}

func (c *Client) Send(ctx context.Context, id, text, key string) (tasks.Task, error) {
	return c.SendTo(ctx, id, "", text, key)
}

func (c *Client) SendTo(ctx context.Context, id, agentID, text, key string) (tasks.Task, error) {
	return c.SendWithAttachments(ctx, id, agentID, text, key, nil)
}

func (c *Client) SendWithAttachments(ctx context.Context, id, agentID, text, key string, files []tasks.Attachment) (tasks.Task, error) {
	return c.SendMessage(ctx, id, key, tasks.MessageRequest{Text: text, AgentID: agentID, Attachments: files})
}

func (c *Client) SendMessage(ctx context.Context, id, key string, input tasks.MessageRequest) (tasks.Task, error) {
	var out tasks.Task
	timeout := 8 * time.Second
	if len(input.Attachments) != 0 {
		timeout = attachmentTransferTimeout
	}
	err := c.callWithTimeout(ctx, timeout, http.MethodPost, "/v1/conversations/"+url.PathEscape(id)+"/messages", key,
		input, &out)
	return out, err
}

func (c *Client) Attachment(ctx context.Context, task string, position int) (tasks.Attachment, error) {
	var out tasks.Attachment
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodGet, "/v1/tasks/"+url.PathEscape(task)+"/attachments/"+strconv.Itoa(position), "", nil, &out)
	return out, err
}

func (c *Client) GeneratedImage(ctx context.Context, task string) (tasks.Attachment, error) {
	var out tasks.Attachment
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodGet, "/v1/tasks/"+url.PathEscape(task)+"/generated-image", "", nil, &out)
	return out, err
}

func (c *Client) Agent(ctx context.Context, id string) (tasks.Agent, error) {
	var out tasks.Agent
	err := c.call(ctx, http.MethodGet, "/v1/agents/"+url.PathEscape(id), "", nil, &out)
	return out, err
}

func (c *Client) SaveAgent(ctx context.Context, a tasks.Agent) (tasks.Agent, error) {
	var out tasks.Agent
	err := c.call(ctx, http.MethodPut, "/v1/agents/"+url.PathEscape(a.ID), "", a, &out)
	return out, err
}

func (c *Client) List(ctx context.Context) ([]tasks.Task, error) {
	var out []tasks.Task
	err := c.call(ctx, http.MethodGet, "/v1/tasks", "", nil, &out)
	return out, err
}

func (c *Client) Create(ctx context.Context, req tasks.Request, key string) (tasks.Task, error) {
	var out tasks.Task
	err := c.call(ctx, http.MethodPost, "/v1/tasks", key, req, &out)
	return out, err
}

func (c *Client) Cancel(ctx context.Context, id string, queuedOnly bool) (tasks.Task, error) {
	var out tasks.Task
	path := "/v1/tasks/" + url.PathEscape(id) + "/cancel"
	if queuedOnly {
		path += "?queued=true"
	}
	err := c.call(ctx, http.MethodPost, path, "", nil, &out)
	return out, err
}

func (c *Client) Steer(ctx context.Context, id, text, key string) error {
	var out tasks.SteeringMessage
	return c.call(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/steer", key,
		struct {
			Text string `json:"text"`
		}{text}, &out)
}

func (c *Client) Events(ctx context.Context, id string, after int64) ([]tasks.Event, error) {
	var out []tasks.Event
	err := c.call(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id)+"/events?after="+strconv.FormatInt(after, 10), "", nil, &out)
	return out, err
}

type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

func (c *Client) call(ctx context.Context, method, path, key string, input, out any) error {
	return c.callWithTimeout(ctx, 8*time.Second, method, path, key, input, out)
}

func (c *Client) callWithTimeout(ctx context.Context, timeout time.Duration, method, path, key string, input, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if err := localconfig.ValidateServerURL(c.base); err != nil {
		return err
	}
	if path != "/healthz" {
		token, err := c.token()
		if err != nil {
			return fmt.Errorf("can't read the Buddy server token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var msg errorBody
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&msg)
		if msg.Error == "" {
			msg.Error = "Buddy server returned " + resp.Status
		}
		return &HTTPError{resp.StatusCode, msg.Error}
	}
	response := io.LimitReader(resp.Body, 16<<20)
	if out == nil {
		_, err := io.Copy(io.Discard, response)
		return err
	}
	return json.NewDecoder(response).Decode(out)
}

type cardAnswer struct {
	Choice *int `json:"choice"`
}

func (c *Client) AnswerCard(ctx context.Context, chat, card string, choice int) error {
	path := "/v1/conversations/" + url.PathEscape(chat) + "/cards/" + url.PathEscape(card) + "/answer"
	return c.call(ctx, http.MethodPost, path, "", cardAnswer{Choice: &choice}, nil)
}
