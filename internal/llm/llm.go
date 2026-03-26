package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/julython/majordomo/internal/config"
)

// Client is the LLM interface used throughout majordomo.
type Client interface {
	Generate(ctx context.Context, prompt string) (string, error)
	// Stream sends tokens to the callback as they arrive. The full
	// response is also returned. If the callback is nil, behaves
	// like Generate.
	Stream(ctx context.Context, prompt string, onChunk func(token string)) (string, error)
	Name() string
}

// LocalClient talks to any OpenAI-compatible API (ollama, llamacpp, lmstudio).
type LocalClient struct {
	baseURL  string
	model    string
	provider string
	client   *http.Client
}

func (c *LocalClient) Name() string { return fmt.Sprintf("%s/%s", c.provider, c.model) }

// Generate waits for the full response (non-streaming).
func (c *LocalClient) Generate(ctx context.Context, prompt string) (string, error) {
	return c.Stream(ctx, prompt, nil)
}

// Stream sends the request with streaming enabled and calls onChunk
// for each token as it arrives from the server.
func (c *LocalClient) Stream(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	streaming := onChunk != nil

	body, _ := json.Marshal(map[string]any{
		"model":    c.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   streaming,
	})

	req, err := http.NewRequestWithContext(ctx,
		"POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c.provider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: status %d", c.provider, resp.StatusCode)
	}

	if !streaming {
		return c.parseFullResponse(resp)
	}

	return c.parseStreamResponse(ctx, resp, onChunk)
}

func (c *LocalClient) parseFullResponse(resp *http.Response) (string, error) {
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("%s: decode: %w", c.provider, err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("%s: empty response", c.provider)
	}
	return result.Choices[0].Message.Content, nil
}

// parseStreamResponse reads SSE-formatted streaming responses.
// Format: "data: {json}\n\n" per chunk, ending with "data: [DONE]\n\n"
func (c *LocalClient) parseStreamResponse(ctx context.Context, resp *http.Response, onChunk func(string)) (string, error) {
	scanner := bufio.NewScanner(resp.Body)
	var full strings.Builder

	for scanner.Scan() {
		if ctx.Err() != nil {
			return full.String(), ctx.Err()
		}

		line := scanner.Text()

		// SSE format: lines starting with "data: "
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Some providers send non-JSON lines, skip them
			continue
		}

		for _, choice := range chunk.Choices {
			token := choice.Delta.Content
			if token != "" {
				full.WriteString(token)
				onChunk(token)
			}
		}
	}

	return full.String(), scanner.Err()
}

// FromConfig creates an LLM client from the user's config.
func FromConfig(cfg *config.Config) Client {
	if cfg.LLM.Provider == "none" || cfg.LLM.Provider == "" {
		return nil
	}

	url := cfg.LLM.URL
	model := cfg.LLM.Model
	provider := cfg.LLM.Provider

	if provider == "auto" {
		detected := detect()
		if detected == nil {
			slog.Info("no LLM detected, running without")
			return nil
		}
		return detected
	}

	probe := &http.Client{Timeout: 2 * time.Second}
	resp, err := probe.Get(url + "/v1/models")
	if err != nil {
		slog.Warn("configured LLM not reachable", "provider", provider, "url", url, "error", err)
		return nil
	}
	resp.Body.Close()

	slog.Info("using configured LLM", "provider", provider, "model", model, "url", url)
	return &LocalClient{
		baseURL:  url,
		model:    model,
		provider: provider,
		client:   &http.Client{Timeout: 5 * time.Minute},
	}
}

func detect() Client {
	probes := []struct {
		name  string
		url   string
		model string
	}{
		{"ollama", "http://localhost:11434", "llama3.2"},
		{"lmstudio", "http://localhost:1234", "default"},
		{"llamacpp", "http://localhost:8080", "default"},
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}

	for _, p := range probes {
		resp, err := httpClient.Get(p.url + "/v1/models")
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			slog.Info("auto-detected LLM", "provider", p.name, "url", p.url)
			return &LocalClient{
				baseURL:  p.url,
				model:    p.model,
				provider: p.name,
				client:   &http.Client{Timeout: 5 * time.Minute},
			}
		}
	}
	return nil
}
