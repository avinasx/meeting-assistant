package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

const (
	geminiDefaultBase = "https://generativelanguage.googleapis.com/v1beta"
	// geminiAPIRevision pins the request/stream schema this client was
	// written against (the step-based Interactions API).
	geminiAPIRevision = "2026-05-20"
)

// gemini talks to the Gemini Interactions API.
type gemini struct{ cfg Config }

func (g *gemini) base() string {
	if g.cfg.BaseURL != "" {
		return g.cfg.BaseURL
	}
	return geminiDefaultBase
}

func (g *gemini) headers() map[string]string {
	return map[string]string{
		"x-goog-api-key": g.cfg.APIKey,
		"Api-Revision":   geminiAPIRevision,
	}
}

func (g *gemini) Stream(ctx context.Context, req Request, onDelta func(string)) error {
	if err := g.cfg.check(true); err != nil {
		return err
	}

	input := make([]map[string]any, 0, len(req.Images)+1)
	for _, img := range req.Images {
		input = append(input, map[string]any{
			"type":      "image",
			"data":      base64.StdEncoding.EncodeToString(img.Data),
			"mime_type": img.MIME,
		})
	}
	input = append(input, map[string]any{"type": "text", "text": req.Text})

	body := map[string]any{
		"model":  g.cfg.Model,
		"input":  input,
		"stream": true,
		// Meeting content is sensitive: do not keep it server-side.
		"store": false,
	}
	if req.System != "" {
		body["system_instruction"] = req.System
	}
	gen := map[string]any{}
	if g.cfg.Reasoning != "" {
		gen["thinking_level"] = g.cfg.Reasoning
	}
	if req.MaxTokens > 0 {
		gen["max_output_tokens"] = req.MaxTokens
	}
	if len(gen) > 0 {
		body["generation_config"] = gen
	}

	resp, err := postJSON(ctx, g.cfg.HTTP, "Gemini", g.base()+"/interactions", g.headers(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gotText := false
	err = readSSE(resp.Body, func(event string, data []byte) error {
		if string(data) == "[DONE]" {
			return errStopStream
		}
		var ev struct {
			EventType string `json:"event_type"`
			Status    string `json:"status"`
			Delta     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Interaction struct {
				Status string `json:"status"`
			} `json:"interaction"`
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil // ignore anything we cannot parse
		}
		kind := ev.EventType
		if kind == "" {
			kind = event
		}
		switch kind {
		case "step.delta":
			if ev.Delta.Type == "text" && ev.Delta.Text != "" {
				gotText = true
				onDelta(ev.Delta.Text)
			}
		case "error":
			msg := ev.Error.Message
			if msg == "" {
				msg = "unknown error"
			}
			if ev.Error.Code != "" {
				msg += " (" + ev.Error.Code + ")"
			}
			return &ProviderError{Provider: "Gemini", Message: msg}
		case "interaction.status_update", "interaction.completed":
			status := ev.Status
			if status == "" {
				status = ev.Interaction.Status
			}
			if status == "failed" || status == "cancelled" {
				return &ProviderError{Provider: "Gemini", Message: "interaction " + status}
			}
		}
		return nil
	})
	return finishStream(ctx, "Gemini", err, gotText)
}

func (g *gemini) ListModels(ctx context.Context) ([]string, error) {
	if g.cfg.APIKey == "" {
		return nil, errNoKey
	}
	var ids []string
	pageToken := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"pageSize": {"1000"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var out struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := getJSON(ctx, g.cfg.HTTP, "Gemini", g.base()+"/models?"+q.Encode(), g.headers(), &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if !nonChatModel(id) {
				ids = append(ids, id)
			}
		}
		if out.NextPageToken == "" {
			break
		}
		pageToken = out.NextPageToken
	}
	return sortedUnique(ids), nil
}
