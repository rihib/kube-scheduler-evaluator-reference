package gpubinpacking

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const FreeCountName = "GPUFreeCountBinPacking"

// FreeCountBinPacking scores nodes by the absolute number of free GPUs:
// the fewer free GPUs a node has, the higher its score (best fit).
// Unlike utilization-percentage bin packing, this keeps large blocks of
// free GPUs intact on big nodes, so nodes with few remaining GPUs (e.g.
// nodes that lost GPUs to failures) are filled first and fragmentation of
// large nodes is avoided.
type FreeCountBinPacking struct {
	handle framework.Handle
}

var _ framework.ScorePlugin = &FreeCountBinPacking{}

func NewFreeCount(_ context.Context, _ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	return &FreeCountBinPacking{handle: handle}, nil
}

func (pl *FreeCountBinPacking) Name() string {
	return FreeCountName
}

// Score returns the raw free GPU count; NormalizeScore inverts it so the
// node with the fewest free GPUs ends up with the highest score.
func (pl *FreeCountBinPacking) Score(
	_ context.Context, _ *framework.CycleState, _ *corev1.Pod, nodeName string,
) (int64, *framework.Status) {
	nodeInfo, err := pl.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.AsStatus(fmt.Errorf("getting node %q from snapshot: %w", nodeName, err))
	}
	capacity, allocated := gpuOf(nodeInfo)
	return capacity - allocated, nil
}

func (pl *FreeCountBinPacking) ScoreExtensions() framework.ScoreExtensions {
	return pl
}

func (pl *FreeCountBinPacking) NormalizeScore(
	_ context.Context, _ *framework.CycleState, _ *corev1.Pod, scores framework.NodeScoreList,
) *framework.Status {
	NormalizeFreeCountScores(scores)
	return nil
}

func NormalizeFreeCountScores(scores framework.NodeScoreList) {
	var maxFree int64
	for _, s := range scores {
		if s.Score > maxFree {
			maxFree = s.Score
		}
	}
	if maxFree == 0 {
		return
	}
	for i := range scores {
		scores[i].Score = framework.MaxNodeScore * (maxFree - scores[i].Score) / maxFree
	}
}
