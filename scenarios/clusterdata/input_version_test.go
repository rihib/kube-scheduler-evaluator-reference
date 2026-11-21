package clusterdata

import (
	"strings"
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const nodesCSVHeader = "sn,cpu_milli,memory_mib,gpu,model\n"
const podsCSVHeader = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n"

func countEventsByType(events []definition.Event, eventType definition.EventType, isNode func(any) bool) int {
	count := 0
	for _, e := range events {
		if e.EventType() == eventType && isNode(e.Object()) {
			count++
		}
	}
	return count
}

func isNodeObject(o any) bool {
	_, ok := o.(*corev1.Node)
	return ok
}

func isReplicaSetObject(o any) bool {
	_, ok := o.(*appsv1.ReplicaSet)
	return ok
}

// jobCreateNames returns the names of the ReplicaSets created by events, in
// event order.
func jobCreateNames(events []definition.Event) []string {
	var names []string
	for _, e := range events {
		if e.EventType() != definition.EventTypeCreate {
			continue
		}
		job, ok := e.Object().(*appsv1.ReplicaSet)
		if !ok {
			continue
		}
		names = append(names, job.Name)
	}
	return names
}

// 入力の版は、ノードの CSV にある行をすべて使う。
func TestBuildInputVersion_UsesAllNodes(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,32000,262144,0,\n" +
		"openb-node-0001,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	nodeCreates := countEventsByType(events, definition.EventTypeCreate, isNodeObject)
	if nodeCreates != 2 {
		t.Errorf("node create events = %d, want 2", nodeCreates)
	}
	if summary.UsedPodCount != 1 {
		t.Errorf("UsedPodCount = %d, want 1", summary.UsedPodCount)
	}
}

// pod_phase が Pending の Pod は入力の版から除く。
func TestBuildInputVersion_ExcludesPendingPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-pending,1000,1024,0,0,,LS,Pending,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if len(names) != 1 || names[0] != "openb-pod-running" {
		t.Errorf("job create names = %v, want [openb-pod-running]", names)
	}
	if summary.UsedPodCount != 1 {
		t.Errorf("UsedPodCount = %d, want 1", summary.UsedPodCount)
	}
	if summary.ExcludedPodCount != 1 {
		t.Errorf("ExcludedPodCount = %d, want 1", summary.ExcludedPodCount)
	}
}

// pod_phase が Failed の Pod は、ほかの Pod と同じく入力の版に残り、実行時間が
// 経ったら完了する対象として数える。
func TestBuildInputVersion_KeepsFailedPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-failed,1000,1024,0,0,,LS,Failed,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if len(names) != 2 {
		t.Errorf("job create names = %v, want 2 names (failed pod kept)", names)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
	if summary.FailedPodCount != 1 {
		t.Errorf("FailedPodCount = %d, want 1", summary.FailedPodCount)
	}
}
