// Command buildinput builds one input version from the openb trace and
// prints a summary of it, so a researcher can check the result of a seed
// and target rho before using the version in an experiment. With --out it
// also writes the version itself, so which version an experiment ran
// against stays on disk afterwards.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args in the same shape as the real command line, builds the
// input version, and writes its summary to stdout (or an error to stderr).
// It returns the process exit code. It is a thin adapter: all the judgment
// about what an input version is lives in clusterdata.BuildInputVersion.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("buildinput", flag.ContinueOnError)
	fs.SetOutput(stderr)

	nodesPath := fs.String("nodes", ".cache/clusterdata/openb_node_list_all_node.csv", "openb node list CSV")
	podsPath := fs.String("pods", ".cache/clusterdata/openb_pod_list_default.csv", "openb pod list CSV")
	seed := fs.Int64("seed", 1, "seed for choosing which nodes of each shape to keep, and for ordering pods")
	targetRho := fs.Float64("rho", 1.0, "target rho the submission intervals are scaled to reach")
	nodeShrinkRatio := fs.Float64("shrink", 0.05, "fraction of each node shape to keep (every shape keeps at least one node)")
	outPath := fs.String("out", "", "path to write the input version itself to, as JSON Lines (empty: only print the summary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	nodesFile, err := os.Open(*nodesPath)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}
	defer nodesFile.Close()

	// openb's node list covers every node, GPU or not. spec.md's decision 8
	// and units/U02.md build input versions from the GPU-only subset of it
	// (1213 of 1523 nodes), so filter it before BuildInputVersion sees it.
	gpuNodes, err := clusterdata.FilterGPUNodes(nodesFile)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}

	podsFile, err := os.Open(*podsPath)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}
	defer podsFile.Close()

	events, summary, err := clusterdata.BuildInputVersion(gpuNodes, podsFile, *seed, *targetRho, *nodeShrinkRatio)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}

	if *outPath != "" {
		if err := writeInputVersion(*outPath, events); err != nil {
			fmt.Fprintf(stderr, "buildinput: %v\n", err)
			return 1
		}
	}

	fmt.Fprintf(stdout, "selected nodes: %d\n", summary.SelectedNodeCount)
	for _, shape := range summary.NodeShapes {
		fmt.Fprintf(stdout, "  gpu=%d model=%q cpu=%dm memory=%dMi: %d\n", shape.GPUCount, shape.Model, shape.CPUMilli, shape.MemoryMiB, shape.Count)
	}
	fmt.Fprintf(stdout, "used pods:       %d\n", summary.UsedPodCount)
	fmt.Fprintf(stdout, "excluded pending: %d\n", summary.ExcludedPendingPodCount)
	fmt.Fprintf(stdout, "failed pods:     %d\n", summary.FailedPodCount)
	fmt.Fprintf(stdout, "multiplier:      %.6f\n", summary.Multiplier)
	fmt.Fprintf(stdout, "submission period: %s\n", summary.SubmissionPeriod)
	fmt.Fprintf(stdout, "rho (before): cpu=%.6f memory=%.6f gpu=%.6f max=%.6f\n",
		summary.RhoBeforeScaling.CPUMilli, summary.RhoBeforeScaling.MemoryMiB, summary.RhoBeforeScaling.GPU, summary.RhoBeforeScaling.Max())
	fmt.Fprintf(stdout, "rho (after):  cpu=%.6f memory=%.6f gpu=%.6f max=%.6f\n",
		summary.RhoAfterScaling.CPUMilli, summary.RhoAfterScaling.MemoryMiB, summary.RhoAfterScaling.GPU, summary.RhoAfterScaling.Max())
	return 0
}

// inputVersionEvent is one event of an input version, in the shape written to
// the --out file: the event's type, the wait before it fires, and the object
// it creates or deletes. definition.Event is an interface whose fields are
// unexported, so it cannot be marshalled directly.
type inputVersionEvent struct {
	EventType string `json:"event_type"`
	Interval  string `json:"interval"`
	Object    any    `json:"object"`
}

// writeInputVersion writes the input version to path as JSON Lines, one event
// per line, creating the parent directories if needed. Writing the version
// itself (not only the summary) is what lets a later run say which version an
// experiment was run against (units/U05.md).
func writeInputVersion(path string, events []definition.Event) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create the directory for %s: %w", path, err)
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for i, event := range events {
		line := inputVersionEvent{
			EventType: event.EventType().String(),
			Interval:  event.Interval().String(),
			Object:    event.Object(),
		}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("failed to write event %d of %s: %w", i, path, err)
		}
	}
	// Close explicitly as well as via defer: a buffered write that only fails
	// at close (a full disk, for instance) would otherwise be reported as a
	// success, leaving a truncated input version behind.
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", path, err)
	}
	return nil
}
