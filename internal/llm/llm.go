// Package llm is a small, provider-agnostic streaming client for the chat
// APIs of Google Gemini, Anthropic and OpenAI-compatible servers.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Image is an image attached to a request.
type Image struct {
	MIME string
	Data []byte
}

// Request is a single-turn, multimodal request.
type Request struct {
	System    string
	Text      string
	Images    []Image
	MaxTokens int // 0 = provider default
}

// Config selects the model and credentials for a provider.
type Config struct {
	APIKey    string
	Model     string
	BaseURL   string // optional override of the provider endpoint
	Reasoning string // "", "low", "medium" or "high"
	// HTTP is optional; a shared default client is used when nil.
	HTTP *http.Client
}

// Provider streams answers from one model vendor.
type Provider interface {
	// Stream sends the request and calls onDelta for each piece of answer
	// text as it arrives. It returns when the answer is complete.
	Stream(ctx context.Context, req Request, onDelta func(text string)) error
	// ListModels returns the model IDs the API key can use.
	ListModels(ctx context.Context) ([]string, error)
}

// Provider kinds accepted by New.
const (
	KindGemini    = "gemini"
	KindAnthropic = "anthropic"
	KindOpenAI    = "openai"
)

// New returns the provider for kind.
func New(kind string, cfg Config) (Provider, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.HTTP == nil {
		cfg.HTTP = sharedClient
	}
	switch kind {
	case KindGemini:
		return &gemini{cfg: cfg}, nil
	case KindAnthropic:
		return &anthropic{cfg: cfg}, nil
	case KindOpenAI:
		return &openAI{cfg: cfg}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", kind)
}

// sharedClient keeps connections alive between requests so that a follow-up
// question does not pay for a new TLS handshake. There is deliberately no
// overall timeout: streams are bounded by the caller's context instead.
var sharedClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       5 * time.Minute,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

var (
	errNoKey   = errors.New("no API key set for this provider (open Settings)")
	errNoModel = errors.New("no model selected for this provider (open Settings)")
)

func (c Config) check(needKey bool) error {
	if needKey && c.APIKey == "" {
		return errNoKey
	}
	if c.Model == "" {
		return errNoModel
	}
	return nil
}

// postJSON sends body as JSON and returns the response if it has a 2xx
// status; otherwise it returns an error describing the API failure.
func postJSON(ctx context.Context, client *http.Client, name, url string, headers map[string]string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(client, name, req)
}

func getJSON(ctx context.Context, client *http.Client, name, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := do(client, name, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s: unexpected response: %w", name, err)
	}
	return nil
}

func do(client *http.Client, name string, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%s: request failed: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return nil, &APIError{Provider: name, Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	return resp, nil
}

// APIError is a non-2xx response from a provider.
type APIError struct {
	Provider string
	Status   int
	Message  string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s API error (HTTP %d): %s", e.Provider, e.Status, msg)
}

// errorMessage pulls the human-readable message out of an error body. The
// three vendors all use {"error": {"message": "..."}}; some compatible
// servers use {"error": "..."} or {"message": "..."}.
func errorMessage(raw []byte) string {
	var shaped struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &shaped) == nil {
		if len(shaped.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(shaped.Error, &obj) == nil && obj.Message != "" {
				return obj.Message
			}
			var s string
			if json.Unmarshal(shaped.Error, &s) == nil && s != "" {
				return s
			}
		}
		if shaped.Message != "" {
			return shaped.Message
		}
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 400 {
		text = text[:400] + "…"
	}
	return text
}

// errStopStream is returned by an SSE callback to end reading early.
var errStopStream = errors.New("stop stream")

// readSSE parses a server-sent event stream, calling fn once per event with
// the event name (may be empty) and its data payload. Lines of any length
// are supported, since image-bearing or long deltas can be large.
func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var event string
	var data []byte
	hasData := false

	dispatch := func() error {
		defer func() {
			event, data, hasData = "", data[:0], false
		}()
		if !hasData {
			return nil
		}
		return fn(event, data)
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				if derr := dispatch(); derr != nil {
					if errors.Is(derr, errStopStream) {
						return nil
					}
					return derr
				}
			case line[0] == ':':
				// comment / keep-alive
			default:
				field, value, _ := bytes.Cut(line, []byte(":"))
				value = bytes.TrimPrefix(value, []byte(" "))
				switch string(field) {
				case "event":
					event = string(value)
				case "data":
					if hasData {
						data = append(data, '\n')
					}
					data = append(data, value...)
					hasData = true
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A final event without a trailing blank line still counts.
				if derr := dispatch(); derr != nil && !errors.Is(derr, errStopStream) {
					return derr
				}
				return nil
			}
			return err
		}
	}
}

// ErrEmptyAnswer is returned when a stream finishes without any answer text.
var ErrEmptyAnswer = errors.New("the model returned an empty answer")

// ProviderError is an error the provider reported inside a response stream.
type ProviderError struct {
	Provider string
	Message  string
}

func (e *ProviderError) Error() string { return e.Provider + ": " + e.Message }

// finishStream turns the outcome of reading a stream into the error returned
// to the caller: provider-reported errors pass through, transport failures
// are described (or replaced by the context error if the caller cancelled),
// and a stream that produced no text is reported as empty.
func finishStream(ctx context.Context, name string, err error, gotText bool) error {
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s: stream interrupted: %w", name, err)
	}
	if !gotText {
		return ErrEmptyAnswer
	}
	return nil
}

// nonChatModel filters out models that cannot answer a text+image prompt
// (embeddings, speech, image/video generation, ...). It is a name heuristic
// used only to tidy the model picker; the settings field still accepts any
// model ID typed by hand.
func nonChatModel(id string) bool {
	id = strings.ToLower(id)
	for _, marker := range []string{
		"embedding", "tts", "imagen", "veo", "-image", "omni", "transcribe",
		"-live", "lyria", "banana", "aqa", "whisper", "dall-e", "moderation",
		"audio", "realtime", "sora", "gpt-image",
	} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

func sortedUnique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
