package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
)

const openAIDefaultBase = "https://api.openai.com/v1"

// openAI talks to the OpenAI Chat Completions API. Because that API is the
// de-facto standard, pointing BaseURL elsewhere makes this provider work
// with OpenRouter, Groq, Ollama, LM Studio and other compatible servers.
type openAI struct{ cfg Config }

func (o *openAI) base() string {
	if o.cfg.BaseURL != "" {
		return o.cfg.BaseURL
	}
	return openAIDefaultBase
}

func (o *openAI) official() bool {
	return o.cfg.BaseURL == "" || strings.Contains(o.cfg.BaseURL, "api.openai.com")
}

func (o *openAI) headers() map[string]string {
	h := map[string]string{}
	if o.cfg.APIKey != "" {
		h["Authorization"] = "Bearer " + o.cfg.APIKey
	}
	return h
}

func (o *openAI) Stream(ctx context.Context, req Request, onDelta func(string)) error {
	// Local servers (Ollama, LM Studio) need no key, so only the official
	// endpoint insists on one.
	if err := o.cfg.check(o.official()); err != nil {
		return err
	}

	content := make([]map[string]any, 0, len(req.Images)+1)
	content = append(content, map[string]any{"type": "text", "text": req.Text})
	for _, img := range req.Images {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}

	messages := make([]map[string]any, 0, 2)
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": content})

	body := map[string]any{
		"model":    o.cfg.Model,
		"messages": messages,
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		// OpenAI renamed the parameter; compatible servers mostly kept the old name.
		if o.official() {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	if o.cfg.Reasoning != "" {
		body["reasoning_effort"] = o.cfg.Reasoning
	}

	resp, err := postJSON(ctx, o.cfg.HTTP, "OpenAI", o.base()+"/chat/completions", o.headers(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gotText := false
	err = readSSE(resp.Body, func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			return errStopStream
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		if ev.Error != nil {
			msg := ev.Error.Message
			if msg == "" {
				msg = "unknown error"
			}
			return &ProviderError{Provider: "OpenAI", Message: msg}
		}
		for _, c := range ev.Choices {
			if c.Delta.Content != "" {
				gotText = true
				onDelta(c.Delta.Content)
			}
		}
		return nil
	})
	return finishStream(ctx, "OpenAI", err, gotText)
}

func (o *openAI) ListModels(ctx context.Context) ([]string, error) {
	if o.official() && o.cfg.APIKey == "" {
		return nil, errNoKey
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, o.cfg.HTTP, "OpenAI", o.base()+"/models", o.headers(), &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if o.official() && nonChatModel(m.ID) {
			continue
		}
		ids = append(ids, m.ID)
	}
	return sortedUnique(ids), nil
}
