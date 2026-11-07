package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type openAIToolTransport func(*http.Request) (*http.Response, error)

func (f openAIToolTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestImageToolPublishesOneDownloadableChatImage(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"data": []any{map[string]any{"b64_json": pngData.Bytes()}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: openAIToolTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if r.URL.String() != "https://api.openai.com/v1/images/generations" || request["n"] != float64(1) ||
			request["size"] != "1024x1536" || request["output_format"] != "png" {
			t.Fatalf("unexpected image request: %s %+v", r.URL, request)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(result))}, nil
	})}
	const userRequest = "Make a portrait of a cat in a blue raincoat."
	job, _, err := store.Send(ctx, DefaultConversationID, userRequest, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	turn.coordinate, turn.action = true, new(TurnAction)
	turn.imageTool = func(ctx context.Context, request ImageRequest) ([]byte, error) {
		return generateOpenAIImage(ctx, client, "synthetic-test-key", request)
	}
	emit := func(string, string) error { return nil }
	for _, fixture := range []struct {
		output string
		calls  int
	}{
		{`{"reply":"","generate_image":{"prompt":"a cat","orientation":"portrait","evidence":"from a previous conversation"}}`, 0},
		{`{"reply":"","generate_image":{"prompt":"a cat","orientation":"portrait","evidence":"Make a portrait of a cat"},"save_file":{"name":"note.md","text":"mixed"}}`, 0},
		{`{"reply":"","generate_image":{"prompt":"a cat","orientation":"portrait","evidence":"Make a portrait of a cat"}}`, 1},
	} {
		if _, err := turn.complete(ctx, "", "", nil, emit, func(string) (string, error) { return fixture.output, nil }); err != nil || calls != fixture.calls {
			t.Fatalf("expected %d calls, got %d (%v)", fixture.calls, calls, err)
		}
	}
	if err := store.finishTurn(ctx, job.ID, Succeeded, "Replied.", "", turn.action); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(conversation.Messages) != 2 || conversation.Messages[0].GeneratedImage ||
		!conversation.Messages[1].GeneratedImage || conversation.Messages[1].Text == "" {
		t.Fatalf("image not attached to the assistant reply: %+v, %v", conversation.Messages, err)
	}
	file, err := store.GeneratedImage(ctx, job.ID)
	if err != nil || file.Type != "image/png" || !bytes.Equal(file.Data, pngData.Bytes()) {
		t.Fatalf("incorrect image download: %q (%v)", file.Type, err)
	}
}

func TestDecisionsRoutesOnlyToConfidentGroupMembers(t *testing.T) {
	route := GroupRouting{Lead: "lead", Buds: []AgentRef{{ID: "lead", Name: "Lead"}, {ID: "reviewer", Name: "Reviewer"}},
		Message: "Could someone review the tests?"}
	for _, fixture := range []struct{ response, want string }{
		{`{"answers":[{"type":"choice","name":"responding_bud","choice":"reviewer","confidence":0.91}]}`, "reviewer"},
		{`{"answers":[{"type":"choice","name":"responding_bud","choice":"reviewer","confidence":0.5}]}`, ""},
		{`{"answers":[{"type":"choice","name":"responding_bud","choice":"outsider","confidence":1}]}`, ""},
		{`{"answers":[{"type":"refusal","name":"responding_bud"}]}`, ""},
	} {
		client := &http.Client{Transport: openAIToolTransport(func(r *http.Request) (*http.Response, error) {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if r.URL.Path != "/v1/decisions" || request["model"] != "gpt-6-luna" {
				t.Fatalf("unexpected decision request: %s %+v", r.URL, request)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fixture.response))}, nil
		})}
		if got := decideGroupBud(t.Context(), client, "synthetic-test-key", route); got != fixture.want {
			t.Errorf("decision returned %q, want %q", got, fixture.want)
		}
	}
}
