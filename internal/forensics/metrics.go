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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// defaultMetricQueries are PromQL templates evaluated at time of death.
// {{pod}} and {{namespace}} are substituted before the query is sent.
var defaultMetricQueries = map[string]string{
	"memory_bytes":  `max(container_memory_working_set_bytes{pod="{{pod}}",namespace="{{namespace}}"})`,
	"cpu_cores":     `max(rate(container_cpu_usage_seconds_total{pod="{{pod}}",namespace="{{namespace}}"}[2m]))`,
	"restart_total": `max(kube_pod_container_status_restarts_total{pod="{{pod}}",namespace="{{namespace}}"})`,
}

// MetricsConfig is the subset of KoronerConfig the metrics collector needs.
type MetricsConfig struct {
	URL     string
	Queries map[string]string
}

// CollectMetrics runs the configured instant queries against Prometheus at the
// time of death and returns a snapshot. Best-effort: an individual query that
// fails is skipped rather than failing the whole investigation. Returns nil
// when no queries produced a value.
func (c *Collector) CollectMetrics(ctx context.Context, cfg MetricsConfig, namespace, pod string, at time.Time) (*koronerv1alpha1.MetricsSnapshot, error) {
	queries := cfg.Queries
	if len(queries) == 0 {
		queries = defaultMetricQueries
	}

	samples := map[string]string{}
	for label, tmpl := range queries {
		q := strings.NewReplacer("{{pod}}", pod, "{{namespace}}", namespace).Replace(tmpl)
		v, ok, err := c.promInstant(ctx, cfg.URL, q, at)
		if err != nil || !ok {
			continue
		}
		samples[label] = v
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &koronerv1alpha1.MetricsSnapshot{Source: cfg.URL, Samples: samples}, nil
}

// promInstant performs a single Prometheus instant query and returns the first
// scalar value as a string.
func (c *Collector) promInstant(ctx context.Context, base, query string, at time.Time) (string, bool, error) {
	endpoint := strings.TrimRight(base, "/") + "/api/v1/query"
	params := url.Values{}
	params.Set("query", query)
	params.Set("time", fmt.Sprintf("%d", at.Unix()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("prometheus returned %s", resp.Status)
	}

	var out struct {
		Data struct {
			Result []struct {
				Value [2]interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, err
	}
	if len(out.Data.Result) == 0 {
		return "", false, nil
	}
	if s, ok := out.Data.Result[0].Value[1].(string); ok {
		return s, true, nil
	}
	return "", false, nil
}
