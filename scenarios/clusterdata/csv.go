package clusterdata

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	corev1 "k8s.io/api/core/v1"
)

// parseNodeRecords reads the openb node CSV from r and builds a Node and a
// Create event for every row, for the full openb scenario (nodeGenerator,
// which streams these events to a channel). It reuses parseNodeCSVRecords
// for the actual CSV reading, so the two node-CSV readers (this one and
// BuildInputVersion's) do not duplicate the reading loop; the only thing
// this one adds is turning each typed record into a Node (via buildNode,
// gpu2023.go's nvidia.com/gpu representation) and a Create event.
func parseNodeRecords(r io.Reader) ([]*corev1.Node, []definition.Event, error) {
	records, err := parseNodeCSVRecords(r)
	if err != nil {
		return nil, nil, err
	}

	nodes := make([]*corev1.Node, 0, len(records))
	events := make([]definition.Event, 0, len(records))
	for _, rec := range records {
		node := buildNode(rec.name, rec.cpuMilli, rec.memoryMiB, rec.gpuCount, rec.model)
		nodes = append(nodes, node)
		events = append(events, definition.NewEvent(definition.EventTypeCreate, node, defaultInterval))
	}

	return nodes, events, nil
}

// openbPodRecord is one row of the openb pod CSV, parsed into typed fields.
type openbPodRecord struct {
	name          string
	cpuMilli      int64
	memoryMiB     int64
	gpuCount      int64
	gpuMilli      int64
	gpuSpec       string
	qos           string
	phase         string
	creationTime  int64
	deletionTime  int64
	scheduledTime int64
}

// openbNodeRecord is one row of the openb node CSV, parsed into typed
// fields, without building a corev1.Node (BuildInputVersion decides how to
// represent a node's GPUs; parseNodeRecords's callers want the old
// nvidia.com/gpu extended-resource form, see decisions.md's "GPU の
// annotation の形").
type openbNodeRecord struct {
	name      string
	cpuMilli  int64
	memoryMiB int64
	gpuCount  int64
	model     string
}

// parseNodeCSVRecords reads the openb node CSV from r into typed records,
// without building any corev1.Node or deciding how its GPUs are
// represented.
//
// sn,cpu_milli,memory_mib,gpu,model
// openb-node-0229,96000,786432,8,V100M32
func parseNodeCSVRecords(r io.Reader) ([]openbNodeRecord, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var records []openbNodeRecord
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clusterdata: node csv read error: %w", err)
		}
		if len(record) < 5 {
			return nil, fmt.Errorf("clusterdata: malformed node record (len=%d)", len(record))
		}

		name := strings.TrimSpace(record[0])
		if strings.EqualFold(name, "sn") {
			continue
		}

		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return nil, err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return nil, err
		}
		gpuCount, err := parseIntField(record[3], "gpu")
		if err != nil {
			return nil, err
		}
		model := strings.TrimSpace(record[4])

		records = append(records, openbNodeRecord{
			name:      name,
			cpuMilli:  cpuMilli,
			memoryMiB: memoryMiB,
			gpuCount:  gpuCount,
			model:     model,
		})
	}

	return records, nil
}

// FilterGPUNodes reads an openb node CSV from r and returns a new CSV
// containing only its header and the rows whose gpu field is greater than
// 0. openb only publishes one node list, covering every node whether or
// not it has a GPU (openb_node_list_all_node.csv), but spec.md's decision 8
// and units/U02.md build input versions from "GPU のあるノードの一覧"
// specifically (1213 of that file's 1523 rows); BuildInputVersion itself
// stays usable with any node list (a mix of GPU and non-GPU nodes is valid
// input for it, and several of its own tests use CPU/memory-only nodes), so
// callers that want the GPU-only openb list, such as cmd/buildinput, filter
// it with this function first.
func FilterGPUNodes(r io.Reader) (io.Reader, error) {
	records, err := parseNodeCSVRecords(r)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("sn,cpu_milli,memory_mib,gpu,model\n")
	for _, n := range records {
		if n.gpuCount <= 0 {
			continue
		}
		fmt.Fprintf(&b, "%s,%d,%d,%d,%s\n", n.name, n.cpuMilli, n.memoryMiB, n.gpuCount, n.model)
	}

	return strings.NewReader(b.String()), nil
}

// parseInputVersionPods reads the openb pod CSV from podsCSV into typed
// records, without filtering, sampling, or ordering them (BuildInputVersion
// does that). scheduled_time is empty for Pending pods in the openb trace
// (they were never scheduled); any other phase with an empty scheduled_time
// is unexpected data, so it is still treated as an error.
//
// name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time
func parseInputVersionPods(podsCSV io.Reader) ([]openbPodRecord, error) {
	reader := csv.NewReader(podsCSV)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var records []openbPodRecord
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clusterdata: pod csv read error: %w", err)
		}
		if len(record) < 11 {
			return nil, fmt.Errorf("clusterdata: malformed pod record (len=%d)", len(record))
		}

		name := strings.TrimSpace(record[0])
		if strings.EqualFold(name, "name") {
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("clusterdata: pod record with empty name")
		}

		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return nil, err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return nil, err
		}
		gpuCount, err := parseIntField(record[3], "num_gpu")
		if err != nil {
			return nil, err
		}
		gpuMilli, err := parseIntField(record[4], "gpu_milli")
		if err != nil {
			return nil, err
		}
		gpuSpec := strings.TrimSpace(record[5])
		qos := strings.TrimSpace(record[6])
		phase := strings.TrimSpace(record[7])
		creationTime, err := parseIntField(record[8], "creation_time")
		if err != nil {
			return nil, err
		}
		deletionTime, err := parseIntField(record[9], "deletion_time")
		if err != nil {
			return nil, err
		}

		var scheduledTime int64
		if strings.EqualFold(phase, "Pending") {
			scheduledTime, err = parseOptionalIntField(record[10], "scheduled_time")
		} else {
			scheduledTime, err = parseIntField(record[10], "scheduled_time")
		}
		if err != nil {
			return nil, err
		}
		if deletionTime < scheduledTime {
			return nil, fmt.Errorf("clusterdata: negative execution duration for %s", name)
		}

		records = append(records, openbPodRecord{
			name:          name,
			cpuMilli:      cpuMilli,
			memoryMiB:     memoryMiB,
			gpuCount:      gpuCount,
			gpuMilli:      gpuMilli,
			gpuSpec:       gpuSpec,
			qos:           qos,
			phase:         phase,
			creationTime:  creationTime,
			deletionTime:  deletionTime,
			scheduledTime: scheduledTime,
		})
	}

	return records, nil
}

// parseOptionalIntField parses field like parseIntField, except an empty
// value parses as 0 instead of failing.
func parseOptionalIntField(raw, field string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseIntField(raw, field)
}
