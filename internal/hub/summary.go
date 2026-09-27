package hub

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed summary_prompt.txt
var summaryPrompt string

type FailureSummary struct {
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

type Summarizer struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

func (s *Summarizer) Summarize(ctx context.Context, detail string) (string, error) {
	if len(detail) > 100000 {
		detail = detail[len(detail)-100000:]
		for !utf8.ValidString(detail) && len(detail) > 0 {
			detail = detail[1:]
		}
	}
	payload := map[string]any{
		"model": "gpt-6-luna", "instructions": summaryPrompt,
		"input":     "Log (complete or last 100 kB):\n" + detail,
		"reasoning": map[string]string{"effort": "none"}, "max_output_tokens": 256, "store": false,
		"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "error_summary", "strict": true,
			"schema": map[string]any{"type": "object", "properties": map[string]any{"summary": map[string]any{"type": []string{"string", "null"}, "maxLength": 280}}, "required": []string{"summary"}, "additionalProperties": false}}},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	base := s.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/responses", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("summary API returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if result.Status != "completed" {
		return "", fmt.Errorf("summary response not completed")
	}
	var text string
	for _, item := range result.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text += content.Text
			}
		}
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal([]byte(text), &value); err != nil {
		return "", err
	}
	raw, ok := value["summary"]
	if !ok || len(value) != 1 {
		return "", fmt.Errorf("invalid summary object")
	}
	var summary *string
	if err = json.Unmarshal(raw, &summary); err != nil {
		return "", err
	}
	if summary == nil {
		return "", nil
	}
	if utf8.RuneCountInString(*summary) > 280 {
		return "", fmt.Errorf("summary exceeds 280 characters")
	}
	return strings.TrimSpace(*summary), nil
}

// SummarizeFailures claims a failure durably before calling the API. Nulls,
// errors and interrupted requests are terminal too: no repeated charges on poll
// or restart. New failure evidence gets a new identity.
func (s *Server) SummarizeFailures(ctx context.Context) error {
	state, err := s.Store.Read(ctx)
	if err != nil {
		return err
	}
	for _, c := range state.Commits {
		_, failures := state.BuildStatus(c.Repository, c.Revision)
		for _, f := range failures {
			if _, ok := state.Summaries[f.ID]; ok {
				continue
			}
			// Evaluation errors wait for the specific child log, not the parent eval.
			if !strings.Contains(f.URL, "/steps/") || !strings.Contains(f.URL, "/logs/") {
				continue
			}
			claimed := false
			if err = s.Store.Update(ctx, func(st *State) error {
				if _, ok := st.Summaries[f.ID]; !ok {
					st.Summaries[f.ID] = FailureSummary{}
					claimed = true
				}
				return nil
			}); err != nil {
				return err
			}
			if !claimed {
				continue
			}
			summary, e := s.Summarizer.Summarize(ctx, f.Detail)
			saved := FailureSummary{Summary: summary}
			if e != nil {
				saved.Error = e.Error()
				slog.Warn("failure summary", "error", e)
			}
			if err = s.Store.Update(ctx, func(st *State) error { st.Summaries[f.ID] = saved; st.RefreshPullRequests(); return nil }); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Server) RunSummaries(ctx context.Context) {
	if s.Summarizer == nil || s.Summarizer.Token == "" {
		return
	}
	for {
		if err := s.SummarizeFailures(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("failure summaries", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}
