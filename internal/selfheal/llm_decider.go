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

package selfheal

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

// LLMDeciderConfig is the fully resolved LLM backend for the heal decider.
// Mirrors the narrator's LLMConfig but kept separate so the two can use
// different providers/models without sharing state.
type LLMDeciderConfig struct {
	Provider koronerv1alpha1.NarratorProvider
	Model    string
	APIKey   string
	BaseURL  string
}

const (
	llmRequestTimeout       = 30 * time.Second
	llmMaxTokens            = 256
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOpenAIBaseURL    = "https://api.openai.com"
)

// llmDecider asks an external LLM to pick one of the allowed actions.
type llmDecider struct {
	cfg    LLMDeciderConfig
	client *http.Client
}

// NewLLMDecider returns a Decider backed by the given provider. An empty API
// key returns an error so callers fall back to RuleDecider rather than
// silently misbehaving.
func NewLLMDecider(cfg LLMDeciderConfig) (Decider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm decider: missing API key")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm decider: missing model")
	}
	switch cfg.Provider {
	case koronerv1alpha1.NarratorProviderAnthropic:
		if cfg.BaseURL == "" {
			cfg.BaseURL = defaultAnthropicBaseURL
		}
	case koronerv1alpha1.NarratorProviderOpenAI:
		if cfg.BaseURL == "" {
			cfg.BaseURL = defaultOpenAIBaseURL
		}
	default:
		return nil, fmt.Errorf("llm decider: unknown provider %q", cfg.Provider)
	}
	return &llmDecider{cfg: cfg, client: &http.Client{Timeout: llmRequestTimeout}}, nil
}

// Decide builds a prompt listing the allowed actions, calls the provider, and
// parses the JSON reply. On any error (network, parse, action outside the
// allowlist) it returns Skip so the engine can fall back. The menu shown to
// the LLM is pre-filtered to actions the subject kind actually supports - we
// don't want the model picking BumpMemory for a bare Pod.
func (d *llmDecider) Decide(ctx context.Context, obit *koronerv1alpha1.Obituary, allowed []koronerv1alpha1.SelfHealAction) Decision {
	menu := applicableActions(obit.Spec.Subject.Kind, allowed)
	if len(menu) == 0 {
		return Skip("no allowlisted action is supported on kind "+obit.Spec.Subject.Kind, "llm")
	}
	prompt := buildHealPrompt(obit, menu)

	raw, err := d.callProvider(ctx, prompt)
	if err != nil {
		return Skip("llm error: "+err.Error(), "llm")
	}

	action, reason, perr := parseDecisionJSON(raw)
	if perr != nil {
		return Skip("llm parse: "+perr.Error(), "llm")
	}
	if action == "" {
		return Skip("llm declined to act: "+reason, "llm")
	}
	if !actionAllowed(action, menu) {
		return Skip("llm picked unsupported/disallowed action "+string(action), "llm")
	}
	return Decision{Action: action, Reason: "llm: " + reason, DecidedBy: "llm"}
}

// buildHealPrompt distils the obituary facts and the allowed-action menu into
// a deterministic prompt asking for a JSON-only reply.
func buildHealPrompt(obit *koronerv1alpha1.Obituary, allowed []koronerv1alpha1.SelfHealAction) string {
	var b strings.Builder
	b.WriteString("You are KubeCoroner's remediation planner. Pick ONE action to remediate ")
	b.WriteString("the following Kubernetes workload death, or decline if no action is appropriate. ")
	b.WriteString("Reply with strict JSON, no prose: ")
	b.WriteString(`{"action": "<one of the allowed actions or empty string>", "reason": "<one short sentence>"}`)
	b.WriteString("\n\nAllowed actions:\n")
	for _, a := range allowed {
		fmt.Fprintf(&b, "  - %s: %s\n", a, actionDescription(a))
	}
	b.WriteString("\nDeath:\n")
	fmt.Fprintf(&b, "  Subject: %s/%s in %s\n", obit.Spec.Subject.Kind, obit.Spec.Subject.Name, obit.Spec.Subject.Namespace)
	fmt.Fprintf(&b, "  Cause: %s\n", obit.Status.CauseOfDeath)
	fmt.Fprintf(&b, "  Confidence: %s\n", obit.Status.Confidence)
	fmt.Fprintf(&b, "  Occurrences: %d\n", obit.Status.Occurrences)
	if obit.Status.Summary != "" {
		fmt.Fprintf(&b, "  Summary: %s\n", obit.Status.Summary)
	}
	if obit.Status.ExitCode != nil {
		fmt.Fprintf(&b, "  ExitCode: %d\n", *obit.Status.ExitCode)
	}
	if obit.Status.OOMKilled {
		b.WriteString("  OOMKilled: true\n")
	}
	if tail := strings.TrimSpace(obit.Status.LogTail); tail != "" {
		b.WriteString("  LogTail (truncated):\n")
		b.WriteString(truncate(tail, 1500))
		b.WriteString("\n")
	}
	return b.String()
}

func actionDescription(a koronerv1alpha1.SelfHealAction) string {
	switch a {
	case koronerv1alpha1.SelfHealDeletePod:
		return "Force-delete the most recent dead pod so its controller recreates it."
	case koronerv1alpha1.SelfHealRestartWorkload:
		return "Trigger a rollout restart on the owning Deployment/StatefulSet."
	case koronerv1alpha1.SelfHealBumpMemory:
		return "Increase the container's memory limit (only sensible when cause is OOM)."
	case koronerv1alpha1.SelfHealRollbackDeployment:
		return "Roll back the Deployment to the previous ReplicaSet's pod template."
	default:
		return ""
	}
}

// decisionEnvelope is the JSON shape the LLM is asked to return.
type decisionEnvelope struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// parseDecisionJSON extracts the first JSON object from text and returns the
// chosen action (empty string == decline) plus reason.
func parseDecisionJSON(text string) (koronerv1alpha1.SelfHealAction, string, error) {
	body := extractJSONObject(text)
	if body == "" {
		return "", "", fmt.Errorf("no JSON object found in response")
	}
	var env decisionEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return "", "", err
	}
	return koronerv1alpha1.SelfHealAction(strings.TrimSpace(env.Action)), strings.TrimSpace(env.Reason), nil
}

// extractJSONObject pulls the first balanced {...} substring out of arbitrary
// LLM output. Both providers occasionally wrap JSON in markdown fences.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// callProvider dispatches to the right HTTP shape. Tiny wrapper so the
// transport details stay out of Decide.
func (d *llmDecider) callProvider(ctx context.Context, prompt string) (string, error) {
	switch d.cfg.Provider {
	case koronerv1alpha1.NarratorProviderAnthropic:
		return d.callAnthropic(ctx, prompt)
	case koronerv1alpha1.NarratorProviderOpenAI:
		return d.callOpenAI(ctx, prompt)
	default:
		return "", fmt.Errorf("unknown provider %q", d.cfg.Provider)
	}
}

type anthropicReq struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []anthropicMs `json:"messages"`
}

type anthropicMs struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (d *llmDecider) callAnthropic(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(anthropicReq{
		Model:     d.cfg.Model,
		MaxTokens: llmMaxTokens,
		Messages:  []anthropicMs{{Role: "user", Content: prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", d.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out anthropicResp
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
	return text.String(), nil
}

type openAIReq struct {
	Model    string     `json:"model"`
	Messages []openAIMs `json:"messages"`
}

type openAIMs struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResp struct {
	Choices []struct {
		Message openAIMs `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (d *llmDecider) callOpenAI(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(openAIReq{
		Model: d.cfg.Model,
		Messages: []openAIMs{
			{Role: "system", Content: "Reply ONLY with the requested JSON object. Do not include prose."},
			{Role: "user", Content: prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.cfg.APIKey)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out openAIResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("openai: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return out.Choices[0].Message.Content, nil
}
