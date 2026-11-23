// Package schedulerprofile builds the kube-scheduler profile configs
// experiment1's pilot (units/U03.md) compares: NodeResourcesFit scored by
// LeastAllocated or MostAllocated, scoring cpu, memory and nvidia.com/gpu
// with weight 1, and NodeResourcesBalancedAllocation's score disabled so the
// scoring strategy is the only thing that differs between the two pilot
// profiles; every other plugin (filtering included) is left at its default
// (units/U03.md Implementation Decisions: "2つの profile を作る... ほかの採点
// は既定のまま残す").
package schedulerprofile

import (
	"fmt"

	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/latest"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
)

// Strategies are the two NodeResourcesFit scoring strategies the pilot
// compares.
var Strategies = []schedulerconfig.ScoringStrategyType{
	schedulerconfig.LeastAllocated,
	schedulerconfig.MostAllocated,
}

// Pilot builds the scheduler profile config for one of Strategies, along
// with the human-readable description written to the transition record's
// header (definition.RunIdentity.SchedulerProfile). It starts from the
// scheduler's own defaulted profile (latest.Default(), the same defaulting
// a KubeSchedulerConfiguration file goes through) so every plugin this
// pilot does not care about - including the single-plugin extension points
// such as QueueSort - keeps its default instead of being left empty. It
// only disables NodeResourcesBalancedAllocation's score and replaces
// NodeResourcesFit's scoring strategy args. It fails instead of silently
// leaving the default scoring strategy in place if the default profile ever
// stops carrying a NodeResourcesFit PluginConfig to replace.
func Pilot(strategy schedulerconfig.ScoringStrategyType) (schedulerconfig.KubeSchedulerProfile, string, error) {
	cfg, err := latest.Default()
	if err != nil {
		return schedulerconfig.KubeSchedulerProfile{}, "", fmt.Errorf("schedulerprofile: failed to build the default scheduler profile: %w", err)
	}
	profile := *cfg.Profiles[0].DeepCopy()
	// Plugins.Score.Disabled, not Plugins.MultiPoint.Disabled: expandMultiPointPlugins
	// (k8s.io/kubernetes/pkg/scheduler/framework/runtime) builds each
	// extension point's disabled set from that extension point's own
	// Disabled list; MultiPoint.Disabled is only consulted by the v1
	// defaulting latest.Default() already ran, so appending to it here
	// would be silently ignored.
	profile.Plugins.Score.Disabled = append(
		profile.Plugins.Score.Disabled,
		schedulerconfig.Plugin{Name: names.NodeResourcesBalancedAllocation},
	)
	configuredFit := false
	for i, pc := range profile.PluginConfig {
		if pc.Name != names.NodeResourcesFit {
			continue
		}
		profile.PluginConfig[i].Args = &schedulerconfig.NodeResourcesFitArgs{
			ScoringStrategy: &schedulerconfig.ScoringStrategy{
				Type: strategy,
				Resources: []schedulerconfig.ResourceSpec{
					{Name: "cpu", Weight: 1},
					{Name: "memory", Weight: 1},
					{Name: "nvidia.com/gpu", Weight: 1},
				},
			},
		}
		configuredFit = true
	}
	if !configuredFit {
		return schedulerconfig.KubeSchedulerProfile{}, "", fmt.Errorf("schedulerprofile: the default scheduler profile has no %s plugin config to replace", names.NodeResourcesFit)
	}
	description := fmt.Sprintf(
		"NodeResourcesFit(scoringStrategy=%s, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled",
		strategy,
	)
	return profile, description, nil
}
