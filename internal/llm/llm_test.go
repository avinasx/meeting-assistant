package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture records the request a provider sent and replies with a canned body.
type capture struct {
	method string
	path   string
	query  string
	header http.Header
	body   map[string]any
}

func serve(t *testing.T, status int, contentType, reply string) (*httptest.Server, *capture) {
	t.Helper()
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query, got.header = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &got.body); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func collect(t *testing.T, p Provider, req Request) (string, error) {
	t.Helper()
	var b strings.Builder
	err := p.Stream(context.Background(), req, func(s string) { b.WriteString(s) })
	return b.String(), err
}

var testImage = Image{MIME: "image/jpeg", Data: []byte{0xFF, 0xD8, 0xFF}}

const testImageB64 = "/9j/"

func TestReadSSE(t *testing.T) {
	stream := ": keep-alive\n" +
		"event: a\ndata: one\n\n" +
		"data: two\r\ndata: lines\r\n\r\n" +
		"event: c\ndata:no-space\n\n" +
		"data: last-without-blank-line"
	type ev struct{ name, data string }
	var got []ev
	err := readSSE(strings.NewReader(stream), func(event string, data []byte) error {
		got = append(got, ev{event, string(data)})
		return nil
	})
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	want := []ev{{"a", "one"}, {"", "two\nlines"}, {"c", "no-space"}, {"", "last-without-blank-line"}}
	if len(got) != len(want) {
		t.Fatalf("got %d events %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadSSEStopsEarlyAndPropagatesErrors(t *testing.T) {
	stream := "data: 1\n\ndata: 2\n\ndata: 3\n\n"
	n := 0
	if err := readSSE(strings.NewReader(stream), func(string, []byte) error {
		n++
		if n == 2 {
			return errStopStream
		}
		return nil
	}); err != nil || n != 2 {
		t.Fatalf("stop: err=%v n=%d, want nil and 2", err, n)
	}

	boom := errors.New("boom")
	if err := readSSE(strings.NewReader(stream), func(string, []byte) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestReadSSELongLine(t *testing.T) {
	long := strings.Repeat("x", 300_000)
	var got string
	if err := readSSE(strings.NewReader("data: "+long+"\n\n"), func(_ string, data []byte) error {
		got = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got != long {
		t.Fatalf("long line truncated: got %d bytes", len(got))
	}
}

func TestGeminiStream(t *testing.T) {
	reply := `event: interaction.created
data: {"interaction":{"id":"v1_x","status":"in_progress"},"event_type":"interaction.created"}

event: step.start
data: {"index":0,"step":{"type":"thought"},"event_type":"step.start"}

event: step.delta
data: {"index":0,"delta":{"signature":"sig","type":"thought_signature"},"event_type":"step.delta"}

event: step.delta
data: {"index":1,"delta":{"text":"Hello, ","type":"text"},"event_type":"step.delta"}

event: step.delta
data: {"index":1,"delta":{"text":"world.","type":"text"},"event_type":"step.delta"}

event: interaction.completed
data: {"interaction":{"id":"v1_x","status":"completed"},"event_type":"interaction.completed"}

event: done
data: [DONE]

`
	srv, got := serve(t, 200, "text/event-stream", reply)
	p, err := New(KindGemini, Config{APIKey: "k", Model: "gemini-test", BaseURL: srv.URL + "/v1beta/", Reasoning: "low"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := collect(t, p, Request{System: "sys", Text: "question", Images: []Image{testImage}, MaxTokens: 512})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Hello, world." {
		t.Errorf("text = %q", text)
	}

	if got.method != "POST" || got.path != "/v1beta/interactions" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.header.Get("x-goog-api-key") != "k" {
		t.Errorf("missing API key header")
	}
	if got.header.Get("Api-Revision") != geminiAPIRevision {
		t.Errorf("Api-Revision = %q", got.header.Get("Api-Revision"))
	}
	b := got.body
	if b["model"] != "gemini-test" || b["stream"] != true || b["system_instruction"] != "sys" {
		t.Errorf("body basics wrong: %v", b)
	}
	if b["store"] != false {
		t.Errorf("store = %v, want false so the meeting is not retained", b["store"])
	}
	gen, _ := b["generation_config"].(map[string]any)
	if gen["thinking_level"] != "low" || gen["max_output_tokens"] != float64(512) {
		t.Errorf("generation_config = %v", gen)
	}
	input, _ := b["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input = %v, want image + text", input)
	}
	img, _ := input[0].(map[string]any)
	if img["type"] != "image" || img["mime_type"] != "image/jpeg" || img["data"] != testImageB64 {
		t.Errorf("image part = %v", img)
	}
	txt, _ := input[1].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "question" {
		t.Errorf("text part = %v", txt)
	}
}

func TestGeminiOmitsUnsetOptions(t *testing.T) {
	srv, got := serve(t, 200, "text/event-stream", `data: {"delta":{"text":"ok","type":"text"},"event_type":"step.delta"}`+"\n\n")
	p, _ := New(KindGemini, Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	if _, err := collect(t, p, Request{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generation_config", "system_instruction"} {
		if _, present := got.body[key]; present {
			t.Errorf("%s should be omitted when unset: %v", key, got.body[key])
		}
	}
}

func TestGeminiStreamErrorEvent(t *testing.T) {
	reply := `event: step.delta
data: {"delta":{"text":"partial","type":"text"},"event_type":"step.delta"}

event: error
data: {"error":{"message":"Deadline expired","code":"gateway_timeout"},"event_type":"error"}

`
	srv, _ := serve(t, 200, "text/event-stream", reply)
	p, _ := New(KindGemini, Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	text, err := collect(t, p, Request{Text: "q"})
	var pe *ProviderError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Deadline expired") || !strings.Contains(err.Error(), "gateway_timeout") {
		t.Fatalf("err = %v, want provider error with message and code", err)
	}
	if text != "partial" {
		t.Errorf("text before the error = %q", text)
	}
}

func TestAnthropicStream(t *testing.T) {
	reply := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Say "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yes."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	srv, got := serve(t, 200, "text/event-stream", reply)
	p, _ := New(KindAnthropic, Config{APIKey: "ak", Model: "claude-test", BaseURL: srv.URL + "/v1"})
	text, err := collect(t, p, Request{System: "sys", Text: "question", Images: []Image{testImage}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Say yes." {
		t.Errorf("text = %q", text)
	}
	if got.path != "/v1/messages" || got.header.Get("x-api-key") != "ak" || got.header.Get("anthropic-version") != anthropicVersion {
		t.Errorf("request = %s, headers %v", got.path, got.header)
	}
	b := got.body
	if b["model"] != "claude-test" || b["system"] != "sys" || b["stream"] != true {
		t.Errorf("body basics wrong: %v", b)
	}
	if b["max_tokens"] != float64(anthropicDefaultMaxTokens) {
		t.Errorf("max_tokens = %v, want the default because the API requires one", b["max_tokens"])
	}
	msgs, _ := b["messages"].([]any)
	msg, _ := msgs[0].(map[string]any)
	content, _ := msg["content"].([]any)
	if msg["role"] != "user" || len(content) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	img, _ := content[0].(map[string]any)
	src, _ := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != testImageB64 {
		t.Errorf("image block = %v", img)
	}
}

func TestAnthropicStreamError(t *testing.T) {
	reply := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	srv, _ := serve(t, 200, "text/event-stream", reply)
	p, _ := New(KindAnthropic, Config{APIKey: "ak", Model: "m", BaseURL: srv.URL})
	_, err := collect(t, p, Request{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIStream(t *testing.T) {
	reply := `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}

data: {"choices":[{"delta":{"content":"Forty"}}]}

data: {"choices":[{"delta":{"content":"-two"}}]}

data: {"choices":[],"usage":{"total_tokens":5}}

data: [DONE]

`
	srv, got := serve(t, 200, "text/event-stream", reply)
	p, _ := New(KindOpenAI, Config{APIKey: "ok", Model: "gpt-test", BaseURL: srv.URL + "/v1", Reasoning: "low"})
	text, err := collect(t, p, Request{System: "sys", Text: "question", Images: []Image{testImage}, MaxTokens: 100})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Forty-two" {
		t.Errorf("text = %q", text)
	}
	if got.path != "/v1/chat/completions" || got.header.Get("Authorization") != "Bearer ok" {
		t.Errorf("request = %s, auth %q", got.path, got.header.Get("Authorization"))
	}
	b := got.body
	if b["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v", b["reasoning_effort"])
	}
	// A custom base URL is a "compatible" server: it gets the classic name.
	if b["max_tokens"] != float64(100) || b["max_completion_tokens"] != nil {
		t.Errorf("token cap fields wrong: %v / %v", b["max_tokens"], b["max_completion_tokens"])
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	sys, _ := msgs[0].(map[string]any)
	user, _ := msgs[1].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "sys" || user["role"] != "user" {
		t.Errorf("roles wrong: %v", msgs)
	}
	parts, _ := user["content"].([]any)
	imgPart, _ := parts[1].(map[string]any)
	imgURL, _ := imgPart["image_url"].(map[string]any)
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(testImage.Data)
	if imgPart["type"] != "image_url" || imgURL["url"] != want {
		t.Errorf("image part = %v", imgPart)
	}
}

func TestOpenAICompatibleNeedsNoKey(t *testing.T) {
	srv, got := serve(t, 200, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	p, _ := New(KindOpenAI, Config{Model: "llama", BaseURL: srv.URL})
	text, err := collect(t, p, Request{Text: "q"})
	if err != nil || text != "hi" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if got.header.Get("Authorization") != "" {
		t.Errorf("Authorization sent without a key: %q", got.header.Get("Authorization"))
	}
	if _, present := got.body["reasoning_effort"]; present {
		t.Errorf("reasoning_effort should be omitted when unset")
	}
}

func TestMissingKeyOrModel(t *testing.T) {
	for _, kind := range []string{KindGemini, KindAnthropic, KindOpenAI} {
		p, _ := New(kind, Config{Model: "m"})
		if _, err := collect(t, p, Request{Text: "q"}); !errors.Is(err, errNoKey) {
			t.Errorf("%s without key: err = %v", kind, err)
		}
		p, _ = New(kind, Config{APIKey: "k"})
		if _, err := collect(t, p, Request{Text: "q"}); !errors.Is(err, errNoModel) {
			t.Errorf("%s without model: err = %v", kind, err)
		}
	}
	if _, err := New("nope", Config{}); err == nil {
		t.Error("unknown provider kind accepted")
	}
}

func TestAPIErrorStatus(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"error":{"message":"API key not valid","status":"INVALID_ARGUMENT"}}`, "API key not valid"},
		{`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, "invalid x-api-key"},
		{`{"error":"model not found"}`, "model not found"},
		{`{"message":"rate limited"}`, "rate limited"},
		{`<html>Bad Gateway</html>`, "Bad Gateway"},
	}
	for _, tc := range cases {
		srv, _ := serve(t, 401, "application/json", tc.body)
		p, _ := New(KindGemini, Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
		_, err := collect(t, p, Request{Text: "q"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 401 || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("body %s: err = %v, want HTTP 401 containing %q", tc.body, err, tc.want)
		}
	}
}

func TestEmptyStreamIsAnError(t *testing.T) {
	srv, _ := serve(t, 200, "text/event-stream", "data: [DONE]\n\n")
	for _, kind := range []string{KindGemini, KindOpenAI} {
		p, _ := New(kind, Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
		if _, err := collect(t, p, Request{Text: "q"}); !errors.Is(err, ErrEmptyAnswer) {
			t.Errorf("%s: err = %v, want ErrEmptyAnswer", kind, err)
		}
	}
}

func TestCancelledStreamReturnsContextError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	p, _ := New(KindOpenAI, Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	err := p.Stream(ctx, Request{Text: "q"}, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestListModels(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		srv, got := serve(t, 200, "application/json",
			`{"models":[{"name":"models/gemini-b"},{"name":"models/gemini-a"},{"name":"models/gemini-embedding-2"},{"name":"models/gemini-a"}]}`)
		p, _ := New(KindGemini, Config{APIKey: "k", BaseURL: srv.URL + "/v1beta"})
		ids, err := p.ListModels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(ids, ",") != "gemini-a,gemini-b" {
			t.Errorf("ids = %v, want sorted, de-duplicated, without embeddings", ids)
		}
		if got.path != "/v1beta/models" || got.header.Get("x-goog-api-key") != "k" {
			t.Errorf("request = %s", got.path)
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		srv, got := serve(t, 200, "application/json", `{"data":[{"id":"claude-b"},{"id":"claude-a"}],"has_more":false}`)
		p, _ := New(KindAnthropic, Config{APIKey: "k", BaseURL: srv.URL + "/v1"})
		ids, err := p.ListModels(context.Background())
		if err != nil || strings.Join(ids, ",") != "claude-a,claude-b" {
			t.Fatalf("ids=%v err=%v", ids, err)
		}
		if got.path != "/v1/models" || got.header.Get("x-api-key") != "k" {
			t.Errorf("request = %s", got.path)
		}
	})
	t.Run("openai compatible keeps every model", func(t *testing.T) {
		srv, _ := serve(t, 200, "application/json", `{"data":[{"id":"llama3"},{"id":"whisper-large"}]}`)
		p, _ := New(KindOpenAI, Config{BaseURL: srv.URL})
		ids, err := p.ListModels(context.Background())
		if err != nil || strings.Join(ids, ",") != "llama3,whisper-large" {
			t.Fatalf("ids=%v err=%v", ids, err)
		}
	})
	t.Run("needs a key", func(t *testing.T) {
		p, _ := New(KindAnthropic, Config{})
		if _, err := p.ListModels(context.Background()); !errors.Is(err, errNoKey) {
			t.Fatalf("err = %v", err)
		}
	})
}
