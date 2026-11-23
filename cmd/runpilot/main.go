// Command runpilot runs experiment1's pilot (spec.md scenario 3): one openb
// input version, against every combination of target rho and scheduler
// profile, each run the requested number of times. Each run writes its own
// transition record (units/U03.md), whose header line identifies the
// combination it belongs to.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
)

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
//
// TODO(U03): not yet implemented; always reports success without running anything.
func run(
	args []string, stdout, stderr io.Writer,
	recordWriterFactory evaluator.RecordWriterFactory,
	referenceCommitProvider func() (string, error),
) int {
	return 0
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
	name string
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
//
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
