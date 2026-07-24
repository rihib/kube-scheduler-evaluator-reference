BASE := docker/compose.yaml
EXT := docker/compose.ext.yaml

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
build-demo:
	mkdir -p bin
	go build -o bin/gpu-binpacking-demo ./cmd/gpu-binpacking-demo

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
