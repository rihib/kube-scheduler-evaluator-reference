package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"time"
)

const (
	defaultVictoriaMetricsURL = "http://127.0.0.1:8428"
	defaultDashboardURL       = "http://127.0.0.1:3000/d/kubecon-kse/alibaba-gpu-2023"
	scenarioID                = "scenario-gpu2023"
)

func main() {
	printOnly := flag.Bool("print", false, "print the dashboard URL without opening it")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dashboardURL, err := buildDemoURL(
		ctx,
		&http.Client{Timeout: 10 * time.Second},
		defaultVictoriaMetricsURL,
		defaultDashboardURL,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *printOnly {
		fmt.Println(dashboardURL)
		return
	}
	if err := exec.Command("open", dashboardURL).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "open Grafana dashboard: %v\n", err)
		os.Exit(1)
	}
}

func buildDemoURL(
	ctx context.Context,
	client *http.Client,
	victoriaMetricsURL, dashboardURL string,
) (string, error) {
	evaluationID, err := latestEvaluationID(ctx, client, victoriaMetricsURL)
	if err != nil {
		return "", err
	}
	startedAt, finishedAt, err := evaluationRange(
		ctx,
		client,
		victoriaMetricsURL,
		evaluationID,
	)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(dashboardURL)
	if err != nil {
		return "", fmt.Errorf("parse dashboard URL: %w", err)
	}
	query := u.Query()
	query.Set("var-evaluation", evaluationID)
	query.Set("from", strconv.FormatInt(startedAt.Add(-5*time.Second).UnixMilli(), 10))
	query.Set("to", strconv.FormatInt(finishedAt.Add(15*time.Second).UnixMilli(), 10))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func latestEvaluationID(
	ctx context.Context,
	client *http.Client,
	victoriaMetricsURL string,
) (string, error) {
	endpoint, err := url.Parse(victoriaMetricsURL + "/api/v1/label/evaluation_id/values")
	if err != nil {
		return "", fmt.Errorf("parse VictoriaMetrics URL: %w", err)
	}
	query := endpoint.Query()
	query.Set(
		"match[]",
		fmt.Sprintf(
			`kube_scheduler_evaluator_cluster_nodes{scenario_id=%q}`,
			scenarioID,
		),
	)
	endpoint.RawQuery = query.Encode()
	var response struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := getJSON(ctx, client, endpoint.String(), &response); err != nil {
		return "", fmt.Errorf("find latest evaluation: %w", err)
	}
	if response.Status != "success" || len(response.Data) == 0 {
		return "", errors.New("no Alibaba GPU 2023 evaluation found; run 'make demo-run' first")
	}
	sort.Strings(response.Data)
	return response.Data[len(response.Data)-1], nil
}

func evaluationRange(
	ctx context.Context,
	client *http.Client,
	victoriaMetricsURL, evaluationID string,
) (time.Time, time.Time, error) {
	endpoint, err := url.Parse(victoriaMetricsURL + "/api/v1/export")
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse VictoriaMetrics URL: %w", err)
	}
	query := endpoint.Query()
	query.Set(
		"match[]",
		fmt.Sprintf(
			`kube_scheduler_evaluator_cluster_nodes{evaluation_id=%q,scenario_id=%q}`,
			evaluationID,
			scenarioID,
		),
	)
	now := time.Now()
	query.Set("start", strconv.FormatInt(now.Add(-365*24*time.Hour).UnixMilli(), 10))
	query.Set("end", strconv.FormatInt(now.Add(365*24*time.Hour).UnixMilli(), 10))
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("create VictoriaMetrics request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query evaluation range: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return time.Time{}, time.Time{}, fmt.Errorf(
			"query evaluation range: unexpected status %s: %s",
			resp.Status,
			string(body),
		)
	}

	decoder := json.NewDecoder(resp.Body)
	var minTimestamp, maxTimestamp int64
	for {
		var series struct {
			Timestamps []int64 `json:"timestamps"`
		}
		if err := decoder.Decode(&series); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return time.Time{}, time.Time{}, fmt.Errorf("decode evaluation range: %w", err)
		}
		for _, timestamp := range series.Timestamps {
			if minTimestamp == 0 || timestamp < minTimestamp {
				minTimestamp = timestamp
			}
			if timestamp > maxTimestamp {
				maxTimestamp = timestamp
			}
		}
	}
	if minTimestamp == 0 || maxTimestamp == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"no cluster samples found for %s",
			evaluationID,
		)
	}
	return time.UnixMilli(minTimestamp), time.UnixMilli(maxTimestamp), nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("unexpected status %s: %s", resp.Status, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
