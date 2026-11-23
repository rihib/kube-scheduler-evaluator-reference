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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/schedulerprofile"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
)

// traceName identifies the input trace in the transition record's header
// (decisions.md: "入力の情報は、トレースの名前と版..."). This pilot only runs
// against the openb trace.
const traceName = "openb"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, nil, nil))
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
	args []string, stderr io.Writer,
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
	if *iterations < 1 {
		fmt.Fprintf(stderr, "runpilot: --iterations must be at least 1, got %d\n", *iterations)
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
	// The pods CSV's file name (minus extension) is treated as the trace's
	// version (e.g. "openb_pod_list_default"), so it must name the openb
	// trace this pilot only supports; a CSV from another trace would
	// otherwise make the record's header lie about which trace it ran.
	traceVersion := strings.TrimSuffix(filepath.Base(*podsPath), ".csv")
	if !strings.HasPrefix(traceVersion, traceName) {
		fmt.Fprintf(stderr, "runpilot: --pods file name %q does not start with the %s trace name; this pilot only supports the %s trace\n", traceVersion, traceName, traceName)
		return 2
	}

	scenarios, err := pilotScenarios(nodesCSV, podsCSV, pilotOptions{
		Seed:            *seed,
		SampleRatio:     *sampleRatio,
		Rhos:            rhos,
		Iterations:      *iterations,
		TraceVersion:    traceVersion,
		ReferenceCommit: refCommit,
	})
	if err != nil {
		fmt.Fprintf(stderr, "runpilot: %v\n", err)
		return 1
	}

	// evaluator.WithRecordWriterFactory takes priority over
	// WithRecordOutputDir (cmd/evaluator/evaluator.go's
	// resolvedRecordWriterFactory), so WithRecordOutputDir can always be set
	// and only overridden by a test's Fake factory.
	cfgOpts := []evaluator.ConfigOption{evaluator.WithRecordOutputDir(*outDir)}
	if recordWriterFactory != nil {
		cfgOpts = append(cfgOpts, evaluator.WithRecordWriterFactory(recordWriterFactory))
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

// pilotOptions groups the flag values pilotScenarios needs, instead of
// taking them as separate parameters (several of which share a type and so
// could otherwise be swapped at a call site without a compile error).
type pilotOptions struct {
	Seed            int64
	SampleRatio     float64
	Rhos            []float64
	Iterations      int
	TraceVersion    string
	ReferenceCommit string
}

// pilotScenarios builds one definition.Scenario per (rho, profile) pair: the
// input version for rho is built once (units/U03.md: "U02 で作った入力の版を、
// そのまま evaluator のシナリオとして使う") and its events are reused by both
// profiles' scenarios, since a scenario's generator only reads the slice.
func pilotScenarios(nodesCSV, podsCSV []byte, opts pilotOptions) ([]definition.Scenario, error) {
	var scenarios []definition.Scenario
	for _, rho := range opts.Rhos {
		events, _, err := clusterdata.BuildInputVersion(
			bytes.NewReader(nodesCSV), bytes.NewReader(podsCSV), opts.Seed, rho, opts.SampleRatio,
		)
		if err != nil {
			return nil, err
		}
		for _, strategy := range schedulerprofile.Strategies {
			profile, description, err := schedulerprofile.Pilot(strategy)
			if err != nil {
				return nil, err
			}
			identity := definition.RunIdentity{
				TraceName:        traceName,
				TraceVersion:     opts.TraceVersion,
				ReferenceCommit:  opts.ReferenceCommit,
				InputSeed:        strconv.FormatInt(opts.Seed, 10),
				Rho:              rho,
				SchedulerProfile: description,
			}
			scenarios = append(scenarios, evaluator.WithScenario(
				scenarioID(rho, strategy), opts.Iterations, eventsGenerator(events), nil, identity,
				scheduler.WithProfiles(profile),
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

// parseRhos splits a comma-separated list of target rho values. ±Inf and NaN
// are rejected even though strconv.ParseFloat accepts them: clusterdata.BuildInputVersion
// only rejects NaN and non-positive values, so +Inf would reach
// definition.NewScenario by way of scenarioID and make it panic on an ID
// containing "+Inf".
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
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("--rhos value %q must be a finite number", part)
		}
		rhos = append(rhos, v)
	}
	if len(rhos) == 0 {
		return nil, fmt.Errorf("--rhos must list at least one value, got %q", s)
	}
	return rhos, nil
}

// scenarioID builds a scenario ID unique to one (rho, profile) combination.
// definition.NewScenario requires an ID matching ^[a-zA-Z][a-zA-Z0-9-]*$, so
// rho's decimal point is replaced with "p" (and a leading minus, which rho
// never has in practice, with "neg" so the id still starts with a letter).
func scenarioID(rho float64, strategy schedulerconfig.ScoringStrategyType) string {
	rhoLabel := strconv.FormatFloat(rho, 'f', -1, 64)
	rhoLabel = strings.ReplaceAll(rhoLabel, "-", "neg")
	rhoLabel = strings.ReplaceAll(rhoLabel, ".", "p")
	return fmt.Sprintf("rho%s-%s", rhoLabel, strings.ToLower(string(strategy)))
}

// referenceCommitFrom reports reference's own commit out of one build
// information value, when it carries one. found is false in a "go test"
// binary, which does not carry a vcs.revision even though nothing is wrong
// with the build info. When the build was made from a dirty working tree
// (vcs.modified), "-dirty" is appended so the record's header does not name
// a commit whose tree does not match what actually ran.
func referenceCommitFrom(info *debug.BuildInfo) (commit string, found bool) {
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit, found = s.Value, true
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if !found {
		return "", false
	}
	if modified {
		commit += "-dirty"
	}
	return commit, true
}

// referenceCommitFromGit reports reference's own commit out of git's own
// answers: "git rev-parse HEAD" for the commit, and "git status --porcelain"
// for whether the working tree is dirty, in which case "-dirty" is appended
// just as referenceCommitFrom does for a dirty build. Git is asked whenever
// build info carries no vcs.revision, which is the normal case for how this
// pilot is run ("go run ./cmd/runpilot", and the Makefile's "go build -o
// bin/... cmd/main.go": only passing a package path to "go build" embeds vcs
// information), so this is the path that usually decides whether the
// record's header names a dirty tree.
func referenceCommitFromGit(revParseOut, statusOut string) string {
	commit := strings.TrimSpace(revParseOut)
	if strings.TrimSpace(statusOut) != "" {
		commit += "-dirty"
	}
	return commit
}

// referenceCommit returns reference's own commit, the same way
// record.EvaluatorCommit does for the evaluator module: build info's
// vcs.revision when available, falling back to asking git directly when it
// is not. Only passing a package path to "go build" embeds vcs information,
// so the git fallback covers more than a "go test" binary: "go run
// ./cmd/runpilot" and the Makefile's "go build -o bin/... cmd/main.go" take
// it too.
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
	// Asked separately because "git rev-parse HEAD" says nothing about
	// uncommitted changes; failing here instead of assuming a clean tree,
	// since a record naming a commit without "-dirty" is a claim about which
	// tree actually ran.
	status, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD succeeded, but git status --porcelain failed, so whether the tree is dirty is unknown: %w", err)
	}
	return referenceCommitFromGit(string(out), string(status)), nil
}
