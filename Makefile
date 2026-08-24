.DEFAULT_GOAL := help
SHELL := /bin/bash

MODULE  := github.com/Foodan-Dev/danshi-backend
BIN     := bin
LDFLAGS := -s -w
GOFLAGS := -trimpath

.PHONY: help
help: ## 显示可用命令
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## 整理依赖
	go mod tidy

.PHONY: fmt
fmt: ## 格式化
	gofmt -w .

.PHONY: lint
lint: ## 静态检查（含分层纪律）
	golangci-lint run ./...

.PHONY: test
test: ## 全量测试（需要 Docker，约 4 分钟）
	go test -race -count=1 ./...

# 排除依赖 testcontainers 的包：model / router / service / testutil / test-contract。
# 本地改代码时用它拿秒级反馈；提交前仍须跑 make test。
.PHONY: test-fast
test-fast: ## 快速测试（不起容器，秒级）
	go test -race -count=1 $(shell go list ./... \
		| grep -vE '/(internal/(model|router|service|testutil)|test/contract)$$')

.PHONY: test-contract
test-contract: ## 独立运行 HTTP 契约黑盒套件（需要 Docker）
	go test -race -count=1 ./test/contract

.PHONY: test-integration
test-integration: ## 集成测试（需要 Docker）
	go test -race -count=1 -tags=integration ./test/...

.PHONY: test-convergence
test-convergence: ## 独立运行 P3 契约、查询预算与端点对账门禁（需要 Docker）
	go test -race -count=1 ./test/contract
	go test -race -count=1 -run '^TestHotPathQueryBudgetsAgainstPostgres$$' ./internal/router
	go test -race -count=1 ./test/convergence

.PHONY: openapi
openapi: ## 重新生成并检查 api/openapi.json 是否漂移
	go run ./cmd/openapi -check -output api/openapi.json

.PHONY: openapi-generate
openapi-generate: ## 更新 api/openapi.json
	go run ./cmd/openapi -output api/openapi.json

.PHONY: cover
cover: ## 测试覆盖率
	go test -count=1 -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

.PHONY: bench
bench: ## 运行不依赖容器的性能基准并报告内存分配
	go test -run '^$$' -bench . -benchmem -short ./...

.PHONY: bench-db
bench-db: ## 运行需要 PostgreSQL testcontainer 的端到端基准
	go test -run '^$$' -bench '^BenchmarkPostListEndToEnd$$' -benchmem ./internal/router

.PHONY: build
build: build-server build-migrate build-jobs build-legacy-migrate ## 构建四个二进制

.PHONY: build-server
build-server:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-server ./cmd/danshi-server

.PHONY: build-migrate
build-migrate:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-migrate ./cmd/danshi-migrate

.PHONY: build-jobs
build-jobs:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-jobs ./cmd/danshi-jobs

.PHONY: build-legacy-migrate
build-legacy-migrate:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-legacy-migrate ./cmd/danshi-legacy-migrate

.PHONY: docker
docker: ## 构建三个镜像
	docker build -f deploy/docker/Dockerfile --target server  -t danshi-server:dev  .
	docker build -f deploy/docker/Dockerfile --target migrate -t danshi-migrate:dev .
	docker build -f deploy/docker/Dockerfile --target jobs    -t danshi-jobs:dev    .

.PHONY: schema-test
schema-test: ## 在隔离容器里跑 schema 回归（两条独立链路）
	@bash scripts/schema_test.sh

.PHONY: clean
clean:
	rm -rf $(BIN) coverage.out
