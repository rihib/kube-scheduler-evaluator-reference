// Command runpilot runs experiment1's pilot (spec.md scenario 3): one openb
// input version, against every combination of target rho and scheduler
// profile, each run the requested number of times. Each run writes its own
// transition record (units/U03.md), whose header line identifies the
// combination it belongs to.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/latest"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
)

// traceName identifies the input trace in the transition record's header
// (decisions.md: "入力の情報は、トレースの名前と版..."). This pilot only runs
// against the openb trace.
const traceName = "openb"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, nil, nil))
}

// run parses args in the same shape as the real command line, builds one
// input version per target rho from the openb CSVs, and runs it against both
// scheduler profiles the requested number of iterations. recordWriterFactory
// and referenceCommitProvider are injected so tests can pass Fakes for where
// the record is written and for reference's own commit (units/U03.md
// Implementation Decisions: "コマンドのアダプターは、記録の書き出し先と、
// reference のコミットを取るものを外から受け取る"); nil selects their real
// implementations.
func run(
	args []string, stdout, stderr io.Writer,
	recordWriterFactory evaluator.RecordWriterFactory,
	referenceCommitProvider func() (string, error),
) int {
	fs := flag.NewFlagSet("runpilot", flag.ContinueOnError)
	fs.SetOutput(stderr)

	nodesPath := fs.String("nodes", ".cache/clusterdata/openb_node_list_all_node.csv", "openb node list CSV")
	podsPath := fs.String("pods", ".cache/clusterdata/openb_pod_list_default.csv", "openb pod list CSV")
	seed := fs.Int64("seed", 1, "seed for sampling and ordering pods")
	sampleRatio := fs.Float64("ratio", 0.8, "fraction of non-Pending pods to keep")
	rhosFlag := fs.String("rhos", "0.5,1.1", "comma-separated target rho values to run, one input version each")
	iterations := fs.Int("iterations", 1, "number of times to repeat each rho x scheduler profile combination")
	outDir := fs.String("out", "records", "directory each run's transition record is written to")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rhos, err := parseRhos(*rhosFlag)
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 2
	}

	nodesCSV, err := os.ReadFile(*nodesPath)
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 1
	}
	podsCSV, err := os.ReadFile(*podsPath)
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 1
	}

	commitProvider := referenceCommitProvider
	if commitProvider == nil {
		commitProvider = referenceCommit
	}
	refCommit, err := commitProvider()
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: failed to get reference commit: %v\n", err)
		return 1
	}
	traceVersion := strings.TrimSuffix(filepath.Base(*podsPath), ".csv")

	scenarios, err := pilotScenarios(nodesCSV, podsCSV, *seed, *sampleRatio, rhos, *iterations, traceVersion, refCommit)
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 1
	}

	var cfgOpts []evaluator.ConfigOption
	if recordWriterFactory != nil {
		cfgOpts = append(cfgOpts, evaluator.WithRecordWriterFactory(recordWriterFactory))
	} else {
		cfgOpts = append(cfgOpts, evaluator.WithRecordOutputDir(*outDir))
	}

	eval, err := evaluator.New(
		evaluator.WithConfig(
			consts.UserID, consts.KubeconfigPath, consts.EtcdPrefix, consts.EtcdURL,
			false, // disable externalMode
			consts.SlogLevel,
			cfgOpts...,
		),
		evaluator.WithMetricStores(),
		scenarios...,
	)
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 1
	}

	return eval.Run(context.Background())
}

// pilotScenarios builds one definition.Scenario per (rho, profile) pair: the
// input version for rho is built once (units/U03.md: "U02 で作った入力の版を、
// そのまま evaluator のシナリオとして使う") and its events are reused by both
// profiles' scenarios, since a scenario's generator only reads the slice.
func pilotScenarios(
	nodesCSV, podsCSV []byte,
	seed int64, sampleRatio float64, rhos []float64, iterations int,
	traceVersion, referenceCommit string,
) ([]definition.Scenario, error) {
	var scenarios []definition.Scenario
	for _, rho := range rhos {
		events, _, err := clusterdata.BuildInputVersion(
			bytes.NewReader(nodesCSV), bytes.NewReader(podsCSV), seed, rho, sampleRatio,
		)
		if err != nil {
			return nil, err
		}
		for _, p := range pilotProfiles {
			schedulerProfile, err := kubeSchedulerProfile(p)
			if err != nil {
				return nil, err
			}
			identity := definition.RunIdentity{
				TraceName:        traceName,
				TraceVersion:     traceVersion,
				ReferenceCommit:  referenceCommit,
				InputSeed:        strconv.FormatInt(seed, 10),
				Rho:              rho,
				SchedulerProfile: profileDescription(p),
			}
			scenarios = append(scenarios, evaluator.WithScenario(
				scenarioID(rho, p), iterations, eventsGenerator(events), nil, identity,
				scheduler.WithProfiles(schedulerProfile),
			))
		}
	}
	return scenarios, nil
}

// eventsGenerator adapts a precomputed event slice (clusterdata.BuildInputVersion's
// result) to the generator function shape evaluator.WithScenario expects.
func eventsGenerator(events []definition.Event) func(chan<- definition.Event) {
	return func(ch chan<- definition.Event) {
		for _, e := range events {
			ch <- e
		}
	}
}

// parseRhos splits a comma-separated list of target rho values.
func parseRhos(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	rhos := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid --rhos value %q: %w", part, err)
		}
		rhos = append(rhos, v)
	}
	if len(rhos) == 0 {
		return nil, fmt.Errorf("--rhos must list at least one value, got %q", s)
	}
	return rhos, nil
}

// pilotProfile is one of the two scheduler profiles compared in experiment1's
// pilot (units/U03.md Implementation Decisions: "2つの profile を作る").
type pilotProfile struct {
	name     string
	strategy schedulerconfig.ScoringStrategyType
}

// pilotProfiles are the two profiles every rho is run against. Both score
// NodeResourcesFit by cpu, memory and nvidia.com/gpu with weight 1, and
// disable NodeResourcesBalancedAllocation's score so NodeResourcesFit's
// scoring strategy is the only thing that differs between them; every other
// plugin (filtering included) is left at its default (units/U03.md).
var pilotProfiles = []pilotProfile{
	{name: "LeastAllocated", strategy: schedulerconfig.LeastAllocated},
	{name: "MostAllocated", strategy: schedulerconfig.MostAllocated},
}

// kubeSchedulerProfile builds the scheduler profile config for one
// pilotProfile, starting from the scheduler's own defaulted profile
// (latest.Default(), the same defaulting a KubeSchedulerConfiguration file
// goes through) so every plugin this pilot does not care about - including
// the single-plugin extension points such as QueueSort - keeps its default
// instead of being left empty. It only disables NodeResourcesBalancedAllocation's
// score and replaces NodeResourcesFit's scoring strategy args (units/U03.md:
// "ほかの採点は既定のまま残す").
func kubeSchedulerProfile(p pilotProfile) (schedulerconfig.KubeSchedulerProfile, error) {
	cfg, err := latest.Default()
	if err != nil {
		return schedulerconfig.KubeSchedulerProfile{}, fmt.Errorf("failed to build the default scheduler profile: %w", err)
	}
	profile := *cfg.Profiles[0].DeepCopy()
	profile.Plugins.MultiPoint.Disabled = append(
		profile.Plugins.MultiPoint.Disabled,
		schedulerconfig.Plugin{Name: names.NodeResourcesBalancedAllocation},
	)
	for i, pc := range profile.PluginConfig {
		if pc.Name != names.NodeResourcesFit {
			continue
		}
		profile.PluginConfig[i].Args = &schedulerconfig.NodeResourcesFitArgs{
			ScoringStrategy: &schedulerconfig.ScoringStrategy{
				Type: p.strategy,
				Resources: []schedulerconfig.ResourceSpec{
					{Name: "cpu", Weight: 1},
					{Name: "memory", Weight: 1},
					{Name: "nvidia.com/gpu", Weight: 1},
				},
			},
		}
	}
	return profile, nil
}

// profileDescription is the human-readable description of p written to the
// transition record's header (definition.RunIdentity.SchedulerProfile).
func profileDescription(p pilotProfile) string {
	return fmt.Sprintf(
		"NodeResourcesFit(scoringStrategy=%s, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled",
		p.strategy,
	)
}

// scenarioID builds a scenario ID unique to one (rho, profile) combination.
// definition.NewScenario requires an ID matching ^[a-zA-Z][a-zA-Z0-9-]*$, so
// rho's decimal point is replaced with "p" (and a leading minus, which rho
// never has in practice, with "neg" so the id still starts with a letter).
func scenarioID(rho float64, p pilotProfile) string {
	rhoLabel := strconv.FormatFloat(rho, 'f', -1, 64)
	rhoLabel = strings.ReplaceAll(rhoLabel, "-", "neg")
	rhoLabel = strings.ReplaceAll(rhoLabel, ".", "p")
	return fmt.Sprintf("rho%s-%s", rhoLabel, strings.ToLower(p.name))
}

// referenceCommitFrom reports reference's own commit out of one build
// information value, when it carries one. found is false in a "go test"
// binary, which does not carry a vcs.revision even though nothing is wrong
// with the build info.
func referenceCommitFrom(info *debug.BuildInfo) (commit string, found bool) {
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value, true
		}
	}
	return "", false
}

// referenceCommit returns reference's own commit, the same way
// record.EvaluatorCommit does for the evaluator module: build info's
// vcs.revision when available (the normal "go build" case), falling back to
// asking git directly for a "go test" binary, which does not carry one.
func referenceCommit() (string, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		if commit, found := referenceCommitFrom(info); found {
			return commit, nil
		}
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("build info has no usable vcs.revision, and git rev-parse HEAD failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
