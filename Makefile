BASE := docker/compose.yaml
EXT := docker/compose.ext.yaml
CLUSTERDATA_DIR := .cache/clusterdata
NODE_LIST := $(CLUSTERDATA_DIR)/openb_node_list_all_node.csv
POD_LIST := $(CLUSTERDATA_DIR)/openb_pod_list_default.csv

.PHONY: all
all: down up run

.PHONY: open
open: demo-open

.PHONY: demo
demo: down up demo-run demo-open

.PHONY: demo-prepare
demo-prepare: $(NODE_LIST) $(POD_LIST)

$(NODE_LIST):
	@mkdir -p $(CLUSTERDATA_DIR)
	curl -fsSL https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_node_list_all_node.csv -o $@

$(POD_LIST):
	@mkdir -p $(CLUSTERDATA_DIR)
	curl -fsSL https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_pod_list_default.csv -o $@

.PHONY: demo-run
demo-run: demo-prepare bin/kubecon-demo
	@echo "Replaying Alibaba GPU 2023: 1,523 nodes, 8,152 tasks..."
	@KSE_NODE_LIST=$(abspath $(NODE_LIST)) KSE_POD_LIST=$(abspath $(POD_LIST)) /usr/bin/time -p ./bin/kubecon-demo
	@echo "Evaluation complete. Opening Grafana with 'make demo-open'."

.PHONY: demo-open
demo-open: bin/demo-open
	@./bin/demo-open

.PHONY: ext
ext: down up-ext run

.PHONY: build
build: bin/kube-scheduler-evaluator

bin/kube-scheduler-evaluator: $(shell find . -name '*.go')
	$(MAKE) do-build

bin/kubecon-demo: $(shell find . -name '*.go')
	go build -o $@ cmd/kubecon/main.go

bin/demo-open: $(shell find cmd/demo-open -name '*.go')
	go build -o $@ cmd/demo-open/main.go

.PHONY: rebuild
rebuild:
	$(MAKE) do-build

do-build:
	go build -o bin/kube-scheduler-evaluator cmd/main.go

.PHONY: run
run: build
	./bin/kube-scheduler-evaluator

.PHONY: up
up:
	docker compose -f $(BASE) up --build -d
# Wait for kube-scheduler to be ready
	@sleep 3

.PHONY: up-ext
up-ext:
	docker compose -f $(BASE) -f $(EXT) up --build -d
	@sleep 5

.PHONY: down
down:
	docker compose -f $(BASE) -f $(EXT) down

.PHONY: clean
clean: down
	rm -rf bin/ log/
