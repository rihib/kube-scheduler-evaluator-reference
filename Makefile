COMPOSE := compose.yaml

.PHONY: all
all: run

.PHONY: ext
ext: down up run

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
	docker compose -f $(COMPOSE) up --build -d
# Wait for kube-scheduler to be ready
	@sleep 5

.PHONY: down
down:
	docker compose -f $(COMPOSE) down

.PHONY: clean
clean: down
	rm -rf bin/ log/
