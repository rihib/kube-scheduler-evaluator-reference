// Package gpubinpacking provides two GPU bin-packing score plugins used to
// demonstrate how a scheduler can be developed, evaluated, and improved with
// kube-scheduler-evaluator.
//
//   - GPUUtilizationBinPacking packs pods onto the node with the highest
//     current GPU utilization percentage. This minimizes the number of nodes
//     in use, but on clusters with heterogeneous GPU counts per node it can
//     fragment large nodes.
//   - GPUFreeCountBinPacking packs pods onto the node with the fewest free
//     GPUs (best fit by absolute free GPU count), which keeps large blocks of
//     free GPUs intact and avoids fragmentation.
package gpubinpacking

import (
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func gpuOf(nodeInfo *framework.NodeInfo) (capacity, allocated int64) {
	if nodeInfo.Allocatable != nil {
		capacity = nodeInfo.Allocatable.ScalarResources[definition.GPUResourceName]
	}
	if nodeInfo.Requested != nil {
		allocated = nodeInfo.Requested.ScalarResources[definition.GPUResourceName]
	}
	if allocated > capacity {
		allocated = capacity
	}
	return capacity, allocated
}
