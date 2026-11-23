package schedulerprofile

import (
	"context"
	"testing"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	schedmetrics "k8s.io/kubernetes/pkg/scheduler/metrics"
)

// expandedPlugins reports the plugins frameworkruntime would actually run
// for profile at every extension point, the same way scheduler.New does
// internally (framework's expandMultiPointPlugins). This is the only way to
// tell whether a change to profile.Plugins actually takes effect: in the
// scheduler config types, MultiPoint.Disabled looks like it should disable a
// plugin at every extension point but is silently ignored once
// latest.Default() has already run (expandMultiPointPlugins reads each
// extension point's own Disabled list instead), while e.g. Score.Disabled
// works; and a plugin can be in effect (e.g. the default QueueSort plugin)
// without profile.Plugins listing it explicitly at all, since the default
// profile only lists plugins it overrides. Reading profile.PluginConfig's
// Args directly would only confirm what Pilot itself wrote, not whether the
// scheduler framework actually honors it, so plugin presence/absence here -
// not Args - is what this test checks.
func expandedPlugins(t *testing.T, profile schedulerconfig.KubeSchedulerProfile) *schedulerconfig.Plugins {
	t.Helper()
	schedmetrics.Register()
	client := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(client, 0)
	f, err := frameworkruntime.NewFramework(context.Background(), plugins.NewInTreeRegistry(), &profile,
		frameworkruntime.WithClientSet(client),
		frameworkruntime.WithInformerFactory(informerFactory),
		frameworkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, nil)),
	)
	if err != nil {
		t.Fatalf("frameworkruntime.NewFramework: %v", err)
	}
	return f.ListPlugins()
}

func pluginNames(set schedulerconfig.PluginSet) []string {
	names := make([]string, len(set.Enabled))
	for i, p := range set.Enabled {
		names[i] = p.Name
	}
	return names
}

// Pilot(strategy) が実際に（frameworkruntimeが読む形で）
// NodeResourcesBalancedAllocationを無効にし、QueueSortなど確認していない
// 拡張点は既定のまま残す。NodeResourcesFitのScoringStrategyの値や資源の重み
// （cpu/memory/nvidia.com/gpuの採用やGPUの重み）は、PluginConfig.Argsという
// 中間のデータを直接読む代わりに、置かれたノードの数という外から見える
// 振る舞いで cmd/runpilot の TestRun_SchedulerProfileAffectsNodePlacement /
// TestRun_SchedulerProfileAffectsGPUNodePlacement が確かめる。
func TestPilot_DisablesBalancedAllocationAndKeepsOtherDefaults(t *testing.T) {
	for _, strategy := range Strategies {
		t.Run(string(strategy), func(t *testing.T) {
			profile, description, err := Pilot(strategy)
			if err != nil {
				t.Fatalf("Pilot: %v", err)
			}
			wantDescription := "NodeResourcesFit(scoringStrategy=" + string(strategy) +
				", resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled"
			if description != wantDescription {
				t.Errorf("description = %q, want %q", description, wantDescription)
			}

			expanded := expandedPlugins(t, profile)
			enabledScore := pluginNames(expanded.Score)
			foundFit := false
			for _, name := range enabledScore {
				if name == names.NodeResourcesBalancedAllocation {
					t.Errorf("enabled Score plugins = %v, must not contain %s", enabledScore, names.NodeResourcesBalancedAllocation)
				}
				if name == names.NodeResourcesFit {
					foundFit = true
				}
			}
			if !foundFit {
				t.Errorf("enabled Score plugins = %v, want it to contain %s", enabledScore, names.NodeResourcesFit)
			}
			if len(pluginNames(expanded.QueueSort)) == 0 {
				t.Errorf("no QueueSort plugin is enabled, want the default profile's queue sort plugin to survive untouched")
			}
		})
	}
}
