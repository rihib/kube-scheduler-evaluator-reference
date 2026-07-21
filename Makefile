BASE := docker/compose.yaml
EXT := docker/compose.ext.yaml

.PHONY: all
all: down up run

.PHONY: open
open:
	@open http://localhost:3000

.PHONY: ext
ext: down up-ext run

# GPU bin-packing demo: compares utilization-percentage bin packing against
# free-GPU-count bin packing on a cluster with heterogeneous GPU counts.
# `make demo` runs both scenarios back to back. To run them one at a time
# (e.g. while presenting), run `make up` once and then
# `make demo-utilization` / `make demo-freecount`.
.PHONY: demo
demo: down up run-demo

.PHONY: run-demo
run-demo: build
	SCENARIOS=demo ./bin/kube-scheduler-evaluator

.PHONY: demo-utilization
demo-utilization: build
	SCENARIOS=demo-utilization ./bin/kube-scheduler-evaluator

.PHONY: demo-freecount
demo-freecount: build
	SCENARIOS=demo-freecount ./bin/kube-scheduler-evaluator

.PHONY: build
build: bin/kube-scheduler-evaluator

bin/kube-scheduler-evaluator: $(shell find . -name '*.go')
	$(MAKE) do-build

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
