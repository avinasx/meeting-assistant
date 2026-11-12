package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
)

const (
	anthropicDefaultBase = "https://api.anthropic.com/v1"
	anthropicVersion     = "2023-06-01"
	// The Messages API requires max_tokens; used when the user set no cap.
	anthropicDefaultMaxTokens = 2048
)

// anthropic talks to the Anthropic Messages API.
type anthropic struct{ cfg Config }

func (a *anthropic) base() string {
	if a.cfg.BaseURL != "" {
		return a.cfg.BaseURL
	}
	return anthropicDefaultBase
}

func (a *anthropic) headers() map[string]string {
	return map[string]string{
		"x-api-key":         a.cfg.APIKey,
		"anthropic-version": anthropicVersion,
	}
}

func (a *anthropic) Stream(ctx context.Context, req Request, onDelta func(string)) error {
	if err := a.cfg.check(true); err != nil {
		return err
	}

	content := make([]map[string]any, 0, len(req.Images)+1)
	for _, img := range req.Images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MIME,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	content = append(content, map[string]any{"type": "text", "text": req.Text})

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body := map[string]any{
		"model":      a.cfg.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   []map[string]any{{"role": "user", "content": content}},
	}
	if req.System != "" {
		body["system"] = req.System
	}

	resp, err := postJSON(ctx, a.cfg.HTTP, "Anthropic", a.base()+"/messages", a.headers(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gotText := false
	err = readSSE(resp.Body, func(event string, data []byte) error {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		kind := ev.Type
		if kind == "" {
			kind = event
		}
		switch kind {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				gotText = true
				onDelta(ev.Delta.Text)
			}
		case "error":
			msg := ev.Error.Message
			if msg == "" {
				msg = "unknown error"
			}
			if ev.Error.Type != "" {
				msg += " (" + ev.Error.Type + ")"
			}
			return &ProviderError{Provider: "Anthropic", Message: msg}
		case "message_stop":
			return errStopStream
		}
		return nil
	})
	return finishStream(ctx, "Anthropic", err, gotText)
}

func (a *anthropic) ListModels(ctx context.Context) ([]string, error) {
	if a.cfg.APIKey == "" {
		return nil, errNoKey
	}
	var ids []string
	after := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := getJSON(ctx, a.cfg.HTTP, "Anthropic", a.base()+"/models?"+q.Encode(), a.headers(), &out); err != nil {
			return nil, err
		}
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
		if !out.HasMore || out.LastID == "" {
			break
		}
		after = out.LastID
	}
	return sortedUnique(ids), nil
}
