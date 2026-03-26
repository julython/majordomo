package knowledge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Entry is a single thing majordomo knows about a repo.
type Entry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`    // "observation", "suggestion", "resolved", "note"
	Topic     string    `json:"topic"`   // "docs", "tests", "ci", "deps", "structure"
	Summary   string    `json:"summary"` // one-line human readable
	Details   string    `json:"details,omitempty"`
	Source    string    `json:"source"` // "scan", "llm", "user"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Resolved  bool      `json:"resolved,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

// Store is the on-disk knowledge base for a single repo.
// Lives at .majordomo/knowledge.json in the repo root.
type Store struct {
	path       string
	Entries    []Entry         `json:"entries"`
	LastScan   time.Time       `json:"last_scan,omitempty"`
	LastReport json.RawMessage `json:"last_report,omitempty"` // the most recent scan.Report
}

func Open(repoRoot string) (*Store, error) {
	dir := filepath.Join(repoRoot, ".majordomo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create .majordomo dir: %w", err)
	}

	s := &Store{path: filepath.Join(dir, "knowledge.json")}

	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		slog.Info("no existing knowledge, starting fresh", "repo", repoRoot)
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read knowledge: %w", err)
	}

	// Try new format (object with entries, last_scan, etc.)
	if err := json.Unmarshal(data, s); err != nil {
		// Fall back to old format (bare array of entries)
		var entries []Entry
		if err2 := json.Unmarshal(data, &entries); err2 != nil {
			return nil, fmt.Errorf("parse knowledge: %w (also tried legacy format: %w)", err, err2)
		}
		s.Entries = entries
		slog.Info("migrated legacy knowledge format", "entries", len(entries))
	}

	slog.Info("loaded knowledge", "entries", len(s.Entries), "repo", repoRoot)
	return s, nil
}

func (s *Store) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Add creates a new entry. Deduplicates by topic+summary.
func (s *Store) Add(e Entry) {
	for i := range s.Entries {
		if s.Entries[i].Topic == e.Topic && s.Entries[i].Summary == e.Summary {
			// Update existing
			s.Entries[i].Details = e.Details
			s.Entries[i].UpdatedAt = time.Now()
			s.Entries[i].Source = e.Source
			return
		}
	}

	if e.ID == "" {
		e.ID = fmt.Sprintf("%s-%d", e.Topic, time.Now().UnixMilli())
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.UpdatedAt = e.CreatedAt
	s.Entries = append(s.Entries, e)
}

// Resolve marks a suggestion as done.
func (s *Store) Resolve(id string) bool {
	for i := range s.Entries {
		if s.Entries[i].ID == id {
			s.Entries[i].Resolved = true
			s.Entries[i].Kind = "resolved"
			s.Entries[i].UpdatedAt = time.Now()
			return true
		}
	}
	return false
}

// Remove deletes an entry by ID.
func (s *Store) Remove(id string) bool {
	for i := range s.Entries {
		if s.Entries[i].ID == id {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// Lookup returns entries filtered by topic and/or kind.
func (s *Store) Lookup(topic, kind string) []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if topic != "" && e.Topic != topic {
			continue
		}
		if kind != "" && e.Kind != kind {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Open returns unresolved suggestions.
func (s *Store) OpenSuggestions() []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Kind == "suggestion" && !e.Resolved {
			out = append(out, e)
		}
	}
	return out
}

// ForLLM returns all knowledge formatted as LLM context.
func (s *Store) ForLLM() string {
	if len(s.Entries) == 0 {
		return ""
	}

	var b []byte
	b = append(b, "## Previously learned about this repo:\n\n"...)
	for _, e := range s.Entries {
		status := e.Kind
		if e.Resolved {
			status = "resolved"
		}
		b = append(b, fmt.Sprintf("- [%s/%s] %s\n", e.Topic, status, e.Summary)...)
		if e.Details != "" {
			b = append(b, fmt.Sprintf("  %s\n", e.Details)...)
		}
	}
	return string(b)
}

// Stats returns a summary of the knowledge base.
func (s *Store) Stats() (total, open, resolved int) {
	for _, e := range s.Entries {
		total++
		if e.Resolved {
			resolved++
		} else if e.Kind == "suggestion" {
			open++
		}
	}
	return
}
