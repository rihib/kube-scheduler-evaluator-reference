package clusterdata

import (
	"strings"
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
)

func TestPodGeneratorExcludesPodsWithoutGPURequests(t *testing.T) {
	const trace = `name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time
cpu-only,1000,1024,0,0,,LS,Running,0,10,0
gpu-one,1000,1024,1,1000,,LS,Running,20,30,20
gpu-two,1000,1024,2,2000,,LS,Running,40,50,40
`
	events := make(chan definition.Event, 3)
	if err := podGeneratorFromReader(events, "test-scheduler", strings.NewReader(trace)); err != nil {
		t.Fatal(err)
	}
	close(events)

	var names []string
	for scenarioEvent := range events {
		replicaSet, ok := scenarioEvent.Object().(*appsv1.ReplicaSet)
		if !ok {
			t.Fatalf("unexpected event object type %T", scenarioEvent.Object())
		}
		names = append(names, replicaSet.Name)
	}
	if got, want := strings.Join(names, ","), "gpu-one,gpu-two"; got != want {
		t.Fatalf("generated Pods = %q, want %q", got, want)
	}
}
