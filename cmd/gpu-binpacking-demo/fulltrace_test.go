package main

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator-reference/plugins/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	demoscenario "github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

func TestFullTraceEmitsGPUMetrics(t *testing.T) {
	if os.Getenv("RUN_FULL_TRACE_TEST") != "1" {
		t.Skip("set RUN_FULL_TRACE_TEST=1 to download and replay the Alibaba trace")
	}

	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanner := bufio.NewScanner(r.Body)
		mu.Lock()
		defer mu.Unlock()
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.Contains(line, `"__name__":"gpu_allocation_percent"`):
				counts["allocation"]++
			case strings.Contains(line, `"__name__":"gpu_average_allocation_percent"`):
				counts["average"]++
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	registry := runtime.Registry{
		gpubinpacking.UtilizationName: gpubinpacking.NewUtilization,
	}
	eval, err := evaluator.New(
		evaluator.WithConfig(
			consts.UserID,
			consts.KubeconfigPath,
			consts.EtcdPrefix,
			consts.EtcdURL,
			false,
			slog.LevelError,
		),
		evaluator.WithMetricStores(
			definition.NewMetricStore(definition.VictoriaMetrics, server.URL),
		),
		evaluator.WithScenario(
			demoscenario.UtilizationScenarioName,
			1,
			demoscenario.UtilizationGenerator,
			registry,
			scheduler.WithProfiles(profile(demoscenario.UtilizationScheduler, gpubinpacking.UtilizationName)),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if code := eval.Run(context.Background()); code != 0 {
		t.Fatalf("evaluation failed with exit code %d", code)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("GPU metric counts: %#v", counts)
	if counts["allocation"] == 0 || counts["average"] == 0 {
		t.Fatalf("full trace emitted no GPU metrics: %#v", counts)
	}
}
