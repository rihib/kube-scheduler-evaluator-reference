BASE := docker/compose.yaml
EXT := docker/compose.ext.yaml
EVALUATOR_DIR := ../kube-scheduler-evaluator

.PHONY: all
all: down up run

.PHONY: open
open:
	@open http://localhost:3000

.PHONY: ext
ext: down up-ext run

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

.PHONY: demo
demo: down up run-demo verify-demo

.PHONY: build-demo
build-demo: check-demo-dependency
	mkdir -p bin
	go build -o bin/gpu-binpacking-demo ./cmd/gpu-binpacking-demo

.PHONY: check-demo-dependency
check-demo-dependency:
	@if ! test -f $(EVALUATOR_DIR)/internal/metric/point/gpuallocation.go; then \
		echo "GPU metric support is missing from $(EVALUATOR_DIR)."; \
		echo "Switch that checkout to agent/kubecon-gpu-binpacking-demo before running the demo:"; \
		echo "  git -C $(EVALUATOR_DIR) fetch origin agent/kubecon-gpu-binpacking-demo"; \
		echo "  git -C $(EVALUATOR_DIR) switch agent/kubecon-gpu-binpacking-demo"; \
		echo "  git -C $(EVALUATOR_DIR) pull --ff-only"; \
		exit 1; \
	fi

.PHONY: run-demo
run-demo: build-demo
	./bin/gpu-binpacking-demo

.PHONY: verify-demo
verify-demo:
	bash ./scripts/verify-gpu-demo.sh

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
