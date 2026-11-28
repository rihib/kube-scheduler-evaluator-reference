// Command run builds one openb input version and runs it through the
// evaluator, writing a transition record (experiment1-pilot's units/U03.md) whose header
// identifies the input version and scheduler profile that produced it.
// It is the common part of "実行をまとめて動かす部分" (units/U01.md): building
// one input version, running the default kube-scheduler profile against it
// the requested number of iterations, and writing each run's record.
//
// A new experiment that needs to run a specific scheduler profile (FGD,
// Tetris, ...) or compare several of them replaces this command with its own
// (units/U01.md "ρ の定義、クラスタの縮め方、GPU の表し方を変えるのは、後の
// 単位で行う" — likewise for which scheduler(s) to run); this command only
// keeps the plumbing every such command needs working.
package main

import (
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
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
)

// traceName identifies the input trace in the transition record's header
// (experiment1-pilot's units/U03.md: "入力の情報は、トレースの名前と版、変換
// 規則の版（reference のコミット）、入力の版のシード、ρ である"). This
// command only runs against the openb trace.
const traceName = "openb"

// schedulerProfile describes, for the transition record's header
// (experiment1-pilot's units/U03.md: "スケジューラの情報は、profile の設定で
// ある"), the scheduler this command runs. It is a constant rather than a
// flag because the command passes no scheduler.Option (see run), so the only
// profile it can ever run is kube-scheduler's default one: a flag could only
// make the header disagree with what actually ran, and aggregate/aggregate.py
// groups runs by this string to diff one scheduler against another. A command
// that runs a specific profile (FGD, Tetris, ...) takes this description from
// the code that configures that profile.
const schedulerProfile = "default kube-scheduler profile"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, nil, nil))
}

// run parses args in the same shape as the real command line, builds one
// input version from the openb CSVs, and runs it the requested number of
// iterations. recordWriterFactory and referenceCommitProvider are injected
// so tests can pass Fakes for where the record is written and for
// reference's own commit (experiment1-pilot's units/U03.md Implementation Decisions: "コマンドの
// アダプターは、記録の書き出し先と、reference のコミットを取るものを外から
// 受け取る"); nil selects their real implementations.
func run(
	args []string, stderr io.Writer,
	recordWriterFactory evaluator.RecordWriterFactory,
	referenceCommitProvider func() (string, error),
) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	nodesPath := fs.String("nodes", ".cache/clusterdata/openb_node_list_all_node.csv", "openb node list CSV")
	podsPath := fs.String("pods", ".cache/clusterdata/openb_pod_list_default.csv", "openb pod list CSV")
	seed := fs.Int64("seed", 1, "seed for choosing which nodes of each shape to keep, and for ordering pods")
	nodeShrinkRatio := fs.Float64("shrink", 0.05, "fraction of each node shape to keep (every shape keeps at least one node)")
	targetRho := fs.Float64("rho", 1.0, "target rho the submission intervals are scaled to reach")
	iterations := fs.Int("iterations", 1, "number of times to repeat the run")
	outDir := fs.String("out", "records", "directory each run's transition record is written to")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// ±Inf and NaN are rejected even though strconv.ParseFloat (which the
	// flag package uses) accepts them: clusterdata.BuildInputVersion only
	// rejects NaN and non-positive values, so +Inf would reach
	// definition.NewScenario by way of scenarioID and make it panic on an ID
	// containing "+Inf".
	if math.IsNaN(*targetRho) || math.IsInf(*targetRho, 0) {
		fmt.Fprintf(stderr, "run: --rho must be a finite number, got %v\n", *targetRho)
		return 2
	}

	if *iterations < 1 {
		fmt.Fprintf(stderr, "run: --iterations must be at least 1, got %d\n", *iterations)
		return 2
	}

	nodesCSV, err := os.ReadFile(*nodesPath)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}
	podsCSV, err := os.ReadFile(*podsPath)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}

	commitProvider := referenceCommitProvider
	if commitProvider == nil {
		commitProvider = referenceCommit
	}
	refCommit, err := commitProvider()
	if err != nil {
		fmt.Fprintf(stderr, "run: failed to get reference commit: %v\n", err)
		return 1
	}
	// The pods CSV's file name (minus extension) is treated as the trace's
	// version (e.g. "openb_pod_list_default"), so it must name the openb
	// trace this command only supports; a CSV from another trace would
	// otherwise make the record's header lie about which trace it ran.
	traceVersion := strings.TrimSuffix(filepath.Base(*podsPath), ".csv")
	if !strings.HasPrefix(traceVersion, traceName) {
		fmt.Fprintf(stderr, "run: --pods file name %q does not start with the %s trace name; this command only supports the %s trace\n", traceVersion, traceName, traceName)
		return 2
	}

	events, _, err := clusterdata.BuildInputVersion(
		strings.NewReader(string(nodesCSV)), strings.NewReader(string(podsCSV)), *seed, *targetRho, *nodeShrinkRatio,
	)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}

	identity := definition.RunIdentity{
		TraceName:        traceName,
		TraceVersion:     traceVersion,
		ReferenceCommit:  refCommit,
		InputSeed:        strconv.FormatInt(*seed, 10),
		Rho:              *targetRho,
		SchedulerProfile: schedulerProfile,
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
		// No scheduler.Option is passed, so the scenario runs the default
		// kube-scheduler profile; a command that needs a specific profile
		// (FGD, Tetris, ...) builds its own scenario with scheduler.WithProfiles
		// instead of this one (units/U01.md).
		evaluator.WithScenario(scenarioID(*targetRho), *iterations, eventsGenerator(events), nil, identity),
	)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}

	return eval.Run(context.Background())
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

// scenarioID builds a scenario ID unique to one target rho.
// definition.NewScenario requires an ID matching ^[a-zA-Z][a-zA-Z0-9-]*$, so
// rho's decimal point is replaced with "p" (and a leading minus, which rho
// never has in practice, with "neg" so the id still starts with a letter).
func scenarioID(rho float64) string {
	rhoLabel := strconv.FormatFloat(rho, 'f', -1, 64)
	rhoLabel = strings.ReplaceAll(rhoLabel, "-", "neg")
	rhoLabel = strings.ReplaceAll(rhoLabel, ".", "p")
	return fmt.Sprintf("rho%s", rhoLabel)
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
// command is run ("go run ./cmd/run": only passing a package path to "go
// build" embeds vcs information), so this is the path that usually decides
// whether the record's header names a dirty tree.
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
// so the git fallback is the path this command takes when run the normal
// way, with "go run ./cmd/run"; a "go test" binary of this package takes it
// too.
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
