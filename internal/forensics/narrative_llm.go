/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package forensics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// LLMConfig describes a fully resolved external-LLM narrator: a provider, a
// model, the API key bytes, and an endpoint override.
type LLMConfig struct {
	Provider koronerv1alpha1.NarratorProvider
	Model    string
	APIKey   string
	BaseURL  string
}

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOpenAIBaseURL    = "https://api.openai.com"
	llmRequestTimeout       = 30 * time.Second
	llmMaxTokens            = 512
)

// NewLLMNarrator builds a Narrator for the configured provider. An unknown
// provider or missing key returns an error so callers can fall back to
// NoopNarrator without surprising silent behaviour.
func NewLLMNarrator(cfg LLMConfig) (Narrator, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm narrator: missing API key")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm narrator: missing model")
	}
	switch cfg.Provider {
	case koronerv1alpha1.NarratorProviderAnthropic:
		base := cfg.BaseURL
		if base == "" {
			base = defaultAnthropicBaseURL
		}
		return &anthropicNarrator{model: cfg.Model, apiKey: cfg.APIKey, baseURL: base, client: defaultHTTPClient()}, nil
	case koronerv1alpha1.NarratorProviderOpenAI:
		base := cfg.BaseURL
		if base == "" {
			base = defaultOpenAIBaseURL
		}
		return &openAINarrator{model: cfg.Model, apiKey: cfg.APIKey, baseURL: base, client: defaultHTTPClient()}, nil
	default:
		return nil, fmt.Errorf("llm narrator: unknown provider %q", cfg.Provider)
	}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: llmRequestTimeout}
}

// buildPrompt distils the structured findings into a compact prompt. We keep
// it deterministic and concise so the model has the facts without drowning in
// the full status object.
func buildPrompt(v Verdict, status *koronerv1alpha1.ObituaryStatus) string {
	var b strings.Builder
	b.WriteString("You are KubeCoroner, a forensic post-mortem writer for dead Kubernetes workloads. ")
	b.WriteString("Write a 2-4 sentence narrative explaining the death in plain English. ")
	b.WriteString("Be factual; do not invent details that are not in the evidence.\n\n")

	fmt.Fprintf(&b, "Cause: %s\n", v.Cause)
	fmt.Fprintf(&b, "Confidence: %s\n", v.Confidence)
	fmt.Fprintf(&b, "Summary: %s\n", v.Summary)
	if status != nil {
		if status.ExitCode != nil {
			fmt.Fprintf(&b, "ExitCode: %d\n", *status.ExitCode)
		}
		if status.Signal != nil {
			fmt.Fprintf(&b, "Signal: %d\n", *status.Signal)
		}
		if status.OOMKilled {
			b.WriteString("OOMKilled: true\n")
		}
		if status.RestartCount > 0 {
			fmt.Fprintf(&b, "RestartCount: %d\n", status.RestartCount)
		}
		if len(status.EventTimeline) > 0 {
			b.WriteString("Events:\n")
			for _, ev := range status.EventTimeline {
				fmt.Fprintf(&b, "  - [%s/%s] %s\n", ev.Type, ev.Reason, oneLine(ev.Message))
			}
		}
		if tail := strings.TrimSpace(status.LogTail); tail != "" {
			b.WriteString("LogTail (last lines):\n")
			b.WriteString(truncate(tail, 2000))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// anthropicNarrator calls the Anthropic Messages API.
type anthropicNarrator struct {
	model   string
	apiKey  string
	baseURL string
	client  *http.Client
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (n *anthropicNarrator) Narrate(ctx context.Context, v Verdict, status *koronerv1alpha1.ObituaryStatus) (string, error) {
	body, err := json.Marshal(anthropicRequest{
		Model:     n.model,
		MaxTokens: llmMaxTokens,
		Messages:  []anthropicMessage{{Role: "user", Content: buildPrompt(v, status)}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", n.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := n.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out anthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic: %s", out.Error.Message)
	}
	var text strings.Builder
	for _, blk := range out.Content {
		if blk.Type == "text" {
			text.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(text.String()), nil
}

// openAINarrator calls the OpenAI chat-completions API.
type openAINarrator struct {
	model   string
	apiKey  string
	baseURL string
	client  *http.Client
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (n *openAINarrator) Narrate(ctx context.Context, v Verdict, status *koronerv1alpha1.ObituaryStatus) (string, error) {
	body, err := json.Marshal(openAIRequest{
		Model: n.model,
		Messages: []openAIMessage{
			{Role: "system", Content: "You are KubeCoroner, a forensic post-mortem writer for dead Kubernetes workloads. Reply with a concise factual narrative; do not invent details."},
			{Role: "user", Content: buildPrompt(v, status)},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.apiKey)

	resp, err := n.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out openAIResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("openai: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// DefaultSecretKey returns the conventional environment-variable-style key
// for a provider's API secret. Callers use it when a SecretKeyRef omits Key.
func DefaultSecretKey(provider koronerv1alpha1.NarratorProvider) string {
	switch provider {
	case koronerv1alpha1.NarratorProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case koronerv1alpha1.NarratorProviderOpenAI:
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}
