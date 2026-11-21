// Command buildinput builds one input version from the openb trace and
// prints a summary of it, so a researcher can check the result of a seed
// and target rho before using the version in an experiment.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
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
	seed := fs.Int64("seed", 1, "seed for sampling and ordering pods")
	targetRho := fs.Float64("rho", 1.0, "target rho the submission intervals are scaled to reach")
	sampleRatio := fs.Float64("ratio", 0.8, "fraction of non-Pending pods to keep")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	nodesFile, err := os.Open(*nodesPath)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}
	defer nodesFile.Close()

	podsFile, err := os.Open(*podsPath)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}
	defer podsFile.Close()

	_, summary, err := clusterdata.BuildInputVersion(nodesFile, podsFile, *seed, *targetRho, *sampleRatio)
	if err != nil {
		fmt.Fprintf(stderr, "buildinput: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "used pods:     %d\n", summary.UsedPodCount)
	fmt.Fprintf(stdout, "excluded pods: %d\n", summary.ExcludedPodCount)
	fmt.Fprintf(stdout, "failed pods:   %d\n", summary.FailedPodCount)
	fmt.Fprintf(stdout, "rho (before):  %.6f\n", summary.RhoBeforeScaling)
	fmt.Fprintf(stdout, "rho (after):   %.6f\n", summary.RhoAfterScaling)
	return 0
}
