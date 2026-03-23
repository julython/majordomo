package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

type Config struct {
	Server ServerConfig `toml:"server"`
	LLM    LLMConfig    `toml:"llm"`
}

type ServerConfig struct {
	URL string `toml:"url"`
}

type LLMConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{URL: "https://api.majordomo.dev"},
		LLM:    LLMConfig{Provider: "auto", Model: "llama3", URL: "http://localhost:11434"},
	}
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = configPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	path := configPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (c *Config) Token() (string, error) {
	return keyring.Get("majordomo", "worker-token")
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "majordomo", "config.toml")
}

func DeviceFlowLogin(ctx context.Context, server string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Post(server+"/api/auth/device", "application/json", nil)
	if err != nil {
		return fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
	}
	json.NewDecoder(resp.Body).Decode(&device)

	fmt.Printf("\nOpen this URL in your browser:\n\n  %s\n\nEnter code: %s\n\nWaiting...\n",
		device.VerificationURI, device.UserCode)

	for {
		time.Sleep(time.Duration(device.Interval) * time.Second)
		body := fmt.Sprintf(`{"device_code":"%s"}`, device.DeviceCode)
		resp, err := client.Post(server+"/api/auth/device/token", "application/json",
			strings.NewReader(body))
		if err != nil {
			continue
		}
		if resp.StatusCode == 200 {
			var token struct {
				AccessToken string `json:"access_token"`
			}
			json.NewDecoder(resp.Body).Decode(&token)
			resp.Body.Close()

			keyring.Set("majordomo", "worker-token", token.AccessToken)
			fmt.Println("Logged in successfully!")
			return nil
		}
		resp.Body.Close()
		if resp.StatusCode != 428 {
			return fmt.Errorf("login failed: %d", resp.StatusCode)
		}
	}
}

func InteractiveSetup(ctx context.Context, cfg *Config) error {
	fmt.Println("\n  Majordomo Setup")
	fmt.Println("  ───────────────")

	// Detect LLM
	fmt.Println("\n  Checking for local LLM servers...")
	candidates := []struct {
		name string
		url  string
	}{
		{"ollama", "http://localhost:11434"},
		{"lmstudio", "http://localhost:1234"},
		{"llamacpp", "http://localhost:8080"},
	}

	probe := &http.Client{Timeout: 2 * time.Second}
	found := false
	for _, c := range candidates {
		resp, err := probe.Get(c.url + "/v1/models")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			fmt.Printf("  ✓ Found %s at %s\n", c.name, c.url)
			cfg.LLM.Provider = c.name
			cfg.LLM.URL = c.url
			found = true
			break
		}
	}
	if !found {
		fmt.Println("  No LLM server detected.")
		fmt.Println("  Install ollama (https://ollama.com) for AI narratives,")
		fmt.Println("  or use --no-llm for stats-only mode.")
		cfg.LLM.Provider = "none"
	}

	// Save
	if err := Save(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  ✓ Config saved to %s\n", configPath())

	// Server (optional)
	fmt.Println("\n  Connect to a Majordomo server? (optional)")
	fmt.Println("  Run `majordomo login` when ready.")
	fmt.Println("\n  Setup complete! Try: majordomo analyze .")
	return nil
}
