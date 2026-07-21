package gpubinpacking

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	UtilizationName = "GPUCurrentUtilization"
	BestFitName     = "GPUBestFit"
)

var gpuResourceName = corev1.ResourceName("nvidia.com/gpu")

// NewUtilization creates the intentionally naive baseline used by the demo.
// It scores the current allocation percentage and deliberately ignores the
// incoming Pod, matching a common but incomplete interpretation of bin packing.
func NewUtilization(_ context.Context, _ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	return &plugin{name: UtilizationName, strategy: currentUtilization, handle: handle}, nil
}

// NewBestFit creates a best-fit scorer. It preserves large contiguous GPU
// slots by preferring the feasible node with the fewest GPUs left after the
// incoming Pod is placed.
func NewBestFit(_ context.Context, _ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	return &plugin{name: BestFitName, strategy: bestFit, handle: handle}, nil
}

type strategy int

const (
	currentUtilization strategy = iota
	bestFit
)

type plugin struct {
	name     string
	strategy strategy
	handle   framework.Handle
}

var _ framework.ScorePlugin = &plugin{}

func (p *plugin) Name() string {
	return p.name
}

func (p *plugin) Score(
	_ context.Context,
	_ *framework.CycleState,
	pod *corev1.Pod,
	nodeName string,
) (int64, *framework.Status) {
	nodeInfo, err := p.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.AsStatus(fmt.Errorf("%s: get node %q: %w", p.name, nodeName, err))
	}
	capacity := nodeInfo.Allocatable.ScalarResources[gpuResourceName]
	allocated := nodeInfo.Requested.ScalarResources[gpuResourceName]
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	podGPU := requests[gpuResourceName]
	requested := podGPU.Value()
	if requested == 0 {
		return 0, nil
	}

	switch p.strategy {
	case currentUtilization:
		return currentUtilizationScore(allocated, capacity), nil
	case bestFit:
		score, err := bestFitScore(allocated, capacity, requested)
		if err != nil {
			return 0, framework.AsStatus(err)
		}
		return score, nil
	default:
		return 0, framework.AsStatus(fmt.Errorf("%s: unknown strategy", p.name))
	}
}

func (p *plugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

func currentUtilizationScore(allocated, capacity int64) int64 {
	if capacity <= 0 || allocated <= 0 {
		return framework.MinNodeScore
	}
	if allocated >= capacity {
		return framework.MaxNodeScore
	}
	return allocated * framework.MaxNodeScore / capacity
}

func bestFitScore(allocated, capacity, requested int64) (int64, error) {
	remaining := capacity - allocated - requested
	if remaining < 0 {
		return 0, fmt.Errorf("GPUBestFit: infeasible GPU allocation: allocated=%d requested=%d capacity=%d", allocated, requested, capacity)
	}
	// An inverse score preserves the absolute-free-GPU ordering without assuming
	// a maximum GPU count per node, while always staying in the framework range.
	return framework.MaxNodeScore / (remaining + 1), nil
}
