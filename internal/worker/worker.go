package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/julython/majordomo/internal/analyze"
	"github.com/julython/majordomo/internal/config"
	"github.com/julython/majordomo/internal/grade"
	"github.com/julython/majordomo/internal/llm"
)

type pollResponse struct {
	Job *job `json:"job"`
}

type job struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type jobResult struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Data   any    `json:"data"`
}

func Run(ctx context.Context, cfg *config.Config, repoPath string, client llm.Client, interval int) error {
	token, err := cfg.Token()
	if err != nil {
		return fmt.Errorf("not logged in: %w", err)
	}

	name, _ := os.Hostname()
	slog.Info("worker started",
		"name", name,
		"repo", repoPath,
		"interval", interval,
		"llm", clientName(client),
	)

	httpClient := &http.Client{Timeout: 30 * time.Second}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		j, err := poll(ctx, httpClient, cfg.Server.URL, token)
		if err != nil {
			slog.Error("poll failed", "error", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		if j == nil {
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		slog.Info("claimed job", "id", j.ID, "kind", j.Kind)

		data, execErr := execute(ctx, j, repoPath, client)

		status := "completed"
		if execErr != nil {
			slog.Error("job failed", "id", j.ID, "error", execErr)
			status = "failed"
			data = map[string]string{"error": execErr.Error()}
		}

		if err := submitResult(ctx, httpClient, cfg.Server.URL, token, j.ID, status, data); err != nil {
			slog.Error("submit failed", "id", j.ID, "error", err)
		}

		slog.Info("job done", "id", j.ID, "status", status)
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func poll(ctx context.Context, client *http.Client, server, token string) (*job, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", server+"/api/workers/poll", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pr pollResponse
	json.NewDecoder(resp.Body).Decode(&pr)
	return pr.Job, nil
}

func submitResult(ctx context.Context, client *http.Client, server, token, jobID, status string, data any) error {
	body, _ := json.Marshal(jobResult{JobID: jobID, Status: status, Data: data})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		server+"/api/workers/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func execute(ctx context.Context, j *job, repoPath string, client llm.Client) (any, error) {
	switch j.Kind {
	case "analyze", "grade":
		return executeAnalyze(ctx, repoPath, client)
	case "health_check":
		return map[string]string{"status": "healthy"}, nil
	default:
		return nil, fmt.Errorf("unknown job kind: %s", j.Kind)
	}
}

func executeAnalyze(ctx context.Context, repoPath string, client llm.Client) (any, error) {
	data, err := analyze.Collect(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	report := grade.FromData(data.ToGradeInput())

	var narrative string
	if client != nil {
		narrative, _ = client.Generate(ctx, analyze.BuildPrompt(data, report))
	}

	return map[string]any{
		"grade":     report,
		"narrative": narrative,
	}, nil
}

func clientName(c llm.Client) string {
	if c == nil {
		return "none"
	}
	return c.Name()
}
