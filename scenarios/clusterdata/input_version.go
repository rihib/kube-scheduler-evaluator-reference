package clusterdata

import (
	"io"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
)

// InputVersionSummary describes how BuildInputVersion turned the openb trace
// into one input version: how many pods ended up in it, how many were left
// out, how many of the used pods were Failed, and the load factor (rho)
// before and after the interval scaling that targets targetRho.
type InputVersionSummary struct {
	UsedPodCount     int
	ExcludedPodCount int
	FailedPodCount   int
	RhoBeforeScaling float64
	RhoAfterScaling  float64
}

// BuildInputVersion reads the openb node and pod CSVs and produces one input
// version: a sequence of events (node creation, pod creation, and the final
// node deletion) together with a summary of how the version was built.
//
// seed selects which sampleRatio of the non-Pending pods are kept and how
// pods sharing the same creation_time are ordered. targetRho is the load
// factor the submission intervals are scaled to reach.
func BuildInputVersion(
	nodesCSV, podsCSV io.Reader,
	seed int64,
	targetRho, sampleRatio float64,
) ([]definition.Event, InputVersionSummary, error) {
	return nil, InputVersionSummary{}, nil
}
