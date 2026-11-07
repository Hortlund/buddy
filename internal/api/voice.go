package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const VoiceProtocol = 18
const VoiceRecoveryProtocol = 21
const VoiceStopWorkProtocol = 46
const voiceKeyPath = "/v1/runtime/voice/key"
const voiceCallPath = "/v1/voice/call"

type VoiceAuth struct {
	Source string `json:"source"`
}

// VoiceEvent is a control or caption. Binary messages contain mono PCM16 at 24 kHz.
type VoiceEvent struct {
	Type         string        `json:"type"`
	Agent        string        `json:"agent,omitempty"`
	Lead         string        `json:"lead,omitempty"`
	Call         string        `json:"call,omitempty"`
	Next         string        `json:"next,omitempty"`
	Speaker      string        `json:"speaker,omitempty"`
	Text         string        `json:"text,omitempty"`
	Item         string        `json:"item,omitempty"`
	Level        float32       `json:"-"`
	Millis       int           `json:"millis,omitempty"`
	Muted        bool          `json:"muted,omitempty"`
	Chat         string        `json:"chat,omitempty"`
	Title        string        `json:"title,omitempty"`
	Participants []VoiceMember `json:"participants,omitempty"`
}

type VoiceMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func voiceRoutes(mux *http.ServeMux, store *tasks.Store, worker *tasks.Worker, key localconfig.VoiceKey, dial realtimeDial, inUse *atomic.Bool) {
	status := func(w http.ResponseWriter, _ *http.Request) {
		_, source, err := key.Read()
		if err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't read the voice credential settings."})
			return
		}
		write(w, http.StatusOK, VoiceAuth{Source: source})
	}
	mux.HandleFunc("GET "+voiceKeyPath, status)
	mux.HandleFunc("PUT "+voiceKeyPath, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"key"`
		}
		if err := decode(w, r, &in, 4<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		in.Key = strings.TrimSpace(in.Key)
		if !localconfig.ValidCodexKey(in.Key) {
			write(w, http.StatusBadRequest, errorBody{"Enter a valid OpenAI API key."})
			return
		}
		if err := key.Save(in.Key); err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't save the voice API key."})
			return
		}
		status(w, r)
	})
	mux.HandleFunc("DELETE "+voiceKeyPath, func(w http.ResponseWriter, r *http.Request) {
		if err := key.Remove(); err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't remove the voice API key."})
			return
		}
		status(w, r)
	})

	mux.HandleFunc("GET "+voiceCallPath, func(w http.ResponseWriter, r *http.Request) {
		apiKey, _, err := key.Read()
		if err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't read the voice credential settings."})
			return
		}
		if !localconfig.ValidCodexKey(apiKey) {
			write(w, http.StatusPreconditionFailed, errorBody{"Add a voice API key in Connections to start calling."})
			return
		}
		if !inUse.CompareAndSwap(false, true) {
			write(w, http.StatusConflict, errorBody{"A call is active or the Bud list is being updated."})
			return
		}
		defer inUse.Store(false)
		chat, err := store.Conversation(r.Context(), r.URL.Query().Get("chat"), 0)
		if err != nil {
			result(w, 0, nil, err)
			return
		}
		if chat.Agent.Removed || chat.Archived {
			write(w, http.StatusBadRequest, errorBody{"This chat has no active Buds."})
			return
		}
		agent := chat.Agent
		if id := r.URL.Query().Get("agent"); id != "" && id != agent.ID {
			for _, profile := range chat.Profiles {
				if profile.ID == id {
					agent = profile
					break
				}
			}
			if agent.ID != id {
				write(w, http.StatusBadRequest, errorBody{"Choose a Bud in this chat to start the call."})
				return
			}
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(16 << 10)
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Minute)
		defer cancel()
		// An upgraded socket outlives its HTTP authentication check. Disconnect
		// paired phones when their access is revoked, including during silence.
		if id := tasks.MobileClientFromContext(ctx); id != "" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					current, err := store.MobileClientID(ctx, token)
					if err != nil || current != id {
						cancel()
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		room := newVoiceRoom(ctx, conn, store, worker, chat, apiKey, dial)
		room.run(agent)
	})
}

func (c *Client) VoiceKey(ctx context.Context, method, key string) (VoiceAuth, error) {
	var out VoiceAuth
	var input any
	if method == http.MethodPut {
		input = struct {
			Key string `json:"key"`
		}{key}
	}
	err := c.call(ctx, method, voiceKeyPath, "", input, &out)
	return out, err
}

type VoiceCall struct{ conn *websocket.Conn }

func (c *Client) OpenVoice(ctx context.Context, chat, agent string) (*VoiceCall, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	query := url.Values{"chat": {chat}, "agent": {agent}}
	conn, res, err := websocket.Dial(ctx, c.base+voiceCallPath+"?"+query.Encode(), &websocket.DialOptions{
		HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		if res != nil {
			defer res.Body.Close()
			var body errorBody
			if json.NewDecoder(res.Body).Decode(&body) == nil && body.Error != "" {
				return nil, &HTTPError{Status: res.StatusCode, Message: body.Error}
			}
		}
		return nil, errors.New("Couldn't connect to the Buddy voice server.")
	}
	conn.SetReadLimit(256 << 10)
	return &VoiceCall{conn}, nil
}

func (c *VoiceCall) Read(ctx context.Context) (event VoiceEvent, pcm []byte, err error) {
	kind, b, err := c.conn.Read(ctx)
	if err != nil {
		return event, nil, err
	}
	if kind == websocket.MessageBinary {
		return event, b, nil
	}
	err = json.Unmarshal(b, &event)
	return
}

func (c *VoiceCall) Send(ctx context.Context, event VoiceEvent) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c.conn, event)
}

func (c *VoiceCall) Audio(ctx context.Context, pcm []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, pcm)
}

func (c *VoiceCall) Close() { _ = c.conn.CloseNow() }
