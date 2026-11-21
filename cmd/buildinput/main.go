// Command buildinput builds one input version from the openb trace and
// prints a summary of it, so a researcher can check the result of a seed
// and target rho before using the version in an experiment.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
)

func main() {
	nodesPath := flag.String("nodes", ".cache/clusterdata/openb_node_list_all_node.csv", "openb node list CSV")
	podsPath := flag.String("pods", ".cache/clusterdata/openb_pod_list_default.csv", "openb pod list CSV")
	seed := flag.Int64("seed", 1, "seed for sampling and ordering pods")
	targetRho := flag.Float64("rho", 1.0, "target rho the submission intervals are scaled to reach")
	sampleRatio := flag.Float64("ratio", 0.8, "fraction of non-Pending pods to keep")
	flag.Parse()

	nodesFile, err := os.Open(*nodesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buildinput: %v\n", err)
		os.Exit(1)
	}
	defer nodesFile.Close()

	podsFile, err := os.Open(*podsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buildinput: %v\n", err)
		os.Exit(1)
	}
	defer podsFile.Close()

	_, summary, err := clusterdata.BuildInputVersion(nodesFile, podsFile, *seed, *targetRho, *sampleRatio)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buildinput: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("used pods:     %d\n", summary.UsedPodCount)
	fmt.Printf("excluded pods: %d\n", summary.ExcludedPodCount)
	fmt.Printf("failed pods:   %d\n", summary.FailedPodCount)
	fmt.Printf("rho (before):  %v\n", summary.RhoBeforeScaling)
	fmt.Printf("rho (after):   %v\n", summary.RhoAfterScaling)
}
