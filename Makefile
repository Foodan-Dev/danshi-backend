.DEFAULT_GOAL := help
SHELL := /bin/bash

MODULE  := github.com/jingyijun/danshi_backend_go
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
test: ## 单元测试
	go test -race -count=1 ./...

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

.PHONY: cover
cover: ## 测试覆盖率
	go test -count=1 -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

.PHONY: build
build: build-server build-migrate ## 构建两个二进制

.PHONY: build-server
build-server:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-server ./cmd/danshi-server

.PHONY: build-migrate
build-migrate:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN)/danshi-migrate ./cmd/danshi-migrate

.PHONY: docker
docker: ## 构建两个镜像
	docker build -f deploy/docker/Dockerfile --target server  -t danshi-server:dev  .
	docker build -f deploy/docker/Dockerfile --target migrate -t danshi-migrate:dev .

.PHONY: schema-test
schema-test: ## 在隔离容器里跑 schema 回归（两条独立链路）
	@bash scripts/schema_test.sh

.PHONY: clean
clean:
	rm -rf $(BIN) coverage.out
