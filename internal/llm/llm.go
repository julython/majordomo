package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/julython/majordomo/internal/config"
)

type Client interface {
	Generate(ctx context.Context, prompt string) (string, error)
	Name() string
}

// Remote talks to any OpenAI-compatible API (ollama, llama.cpp, lmstudio, etc.)
type Remote struct {
	client   *http.Client
	baseURL  string
	model    string
	provider string
}

func (r *Remote) Generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": r.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream":      false,
		"temperature": 0.7,
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		r.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling %s at %s: %w", r.provider, r.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %d", r.provider, resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response from %s", r.provider)
	}
	return result.Choices[0].Message.Content, nil
}

func (r *Remote) Name() string {
	return fmt.Sprintf("%s/%s", r.provider, r.model)
}

// New creates an LLM client based on config, with auto-detection.
func New(cfg *config.Config, modelOverride string) (Client, error) {
	model := cfg.LLM.Model
	if modelOverride != "" {
		model = modelOverride
	}

	switch cfg.LLM.Provider {
	case "ollama", "llamacpp", "lmstudio":
		return &Remote{
			client:   &http.Client{Timeout: 5 * time.Minute},
			baseURL:  cfg.LLM.URL,
			model:    model,
			provider: cfg.LLM.Provider,
		}, nil
	case "none":
		return nil, fmt.Errorf("LLM disabled")
	case "auto", "":
		return autoDetect(model)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.LLM.Provider)
	}
}

func autoDetect(model string) (Client, error) {
	candidates := []struct {
		name  string
		url   string
		model string
	}{
		{"ollama", "http://localhost:11434", "llama3"},
		{"lmstudio", "http://localhost:1234", "default"},
		{"llamacpp", "http://localhost:8080", "default"},
	}

	probe := &http.Client{Timeout: 2 * time.Second}
	for _, c := range candidates {
		resp, err := probe.Get(c.url + "/v1/models")
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			m := c.model
			if model != "" {
				m = model
			}
			slog.Info("detected LLM", "provider", c.name, "url", c.url)
			return &Remote{
				client:   &http.Client{Timeout: 5 * time.Minute},
				baseURL:  c.url,
				model:    m,
				provider: c.name,
			}, nil
		}
	}

	return nil, fmt.Errorf("no LLM server detected — start ollama, lmstudio, or llama.cpp")
}
