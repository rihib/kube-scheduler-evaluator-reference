package gpubinpacking

import (
	"testing"

	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// The situation from the demo scenario: a full 1-GPU node candidate does not
// exist, but a free 1-GPU node (free=1) must beat a half-used 8-GPU node
// (free=4) under free-count bin packing, while utilization bin packing
// prefers the half-used 8-GPU node.
func TestUtilizationScore(t *testing.T) {
	tests := []struct {
		name      string
		capacity  int64
		allocated int64
		want      int64
	}{
		{name: "empty 1-GPU node", capacity: 1, allocated: 0, want: 0},
		{name: "half-used 8-GPU node", capacity: 8, allocated: 4, want: 50},
		{name: "full node", capacity: 8, allocated: 8, want: 100},
		{name: "no GPU node", capacity: 0, allocated: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UtilizationScore(tt.capacity, tt.allocated); got != tt.want {
				t.Errorf("UtilizationScore(%d, %d) = %d, want %d", tt.capacity, tt.allocated, got, tt.want)
			}
		})
	}
}

func TestNormalizeFreeCountScores(t *testing.T) {
	scores := framework.NodeScoreList{
		{Name: "gpu1-node-empty", Score: 1},     // 1-GPU node, all free
		{Name: "gpu8-node-halfused", Score: 4},  // 8-GPU node, 4 free
		{Name: "gpu8-node-empty", Score: 8},     // 8-GPU node, all free
		{Name: "gpu8-node-onefree", Score: 1},   // 8-GPU node, 1 free
	}
	NormalizeFreeCountScores(scores)
	want := map[string]int64{
		"gpu1-node-empty":    87,
		"gpu8-node-halfused": 50,
		"gpu8-node-empty":    0,
		"gpu8-node-onefree":  87,
	}
	for _, s := range scores {
		if s.Score != want[s.Name] {
			t.Errorf("normalized score of %s = %d, want %d", s.Name, s.Score, want[s.Name])
		}
	}
	if scores[0].Score <= scores[1].Score {
		t.Errorf("free-count bin packing must prefer the empty 1-GPU node over the half-used 8-GPU node")
	}
}
