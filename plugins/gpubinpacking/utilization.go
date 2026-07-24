package gpubinpacking

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const UtilizationName = "GPUUtilizationBinPacking"

// UtilizationBinPacking scores nodes by their current GPU utilization
// percentage: the more utilized a node already is, the higher its score.
// The incoming pod's request is intentionally not counted, so the score
// reflects the utilization the node has before this pod is placed.
type UtilizationBinPacking struct {
	handle framework.Handle
}

var _ framework.ScorePlugin = &UtilizationBinPacking{}

func NewUtilization(_ context.Context, _ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	return &UtilizationBinPacking{handle: handle}, nil
}

func (pl *UtilizationBinPacking) Name() string {
	return UtilizationName
}

func (pl *UtilizationBinPacking) Score(
	_ context.Context, _ *framework.CycleState, _ *corev1.Pod, nodeName string,
) (int64, *framework.Status) {
	nodeInfo, err := pl.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.AsStatus(fmt.Errorf("getting node %q from snapshot: %w", nodeName, err))
	}
	return UtilizationScore(gpuOf(nodeInfo)), nil
}

func UtilizationScore(capacity, allocated int64) int64 {
	if capacity <= 0 {
		return 0
	}
	return allocated * framework.MaxNodeScore / capacity
}

func (pl *UtilizationBinPacking) ScoreExtensions() framework.ScoreExtensions {
	return nil
}
