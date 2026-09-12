# 测试指南

旦食后端把 PostgreSQL 的约束、触发器、行锁和事务语义视为系统行为的一部分。完整测试因此需要可用的 Docker daemon；testcontainers 会为测试创建隔离的 PostgreSQL 18 容器，并在测试结束时清理。

## 1. 日常门禁

提交前运行：

```bash
make fmt && make lint && make test && make openapi
```

| 目标 | 实际命令 | 作用 |
|---|---|---|
| `make fmt` | `gofmt -w .` | 格式化全部 Go 文件 |
| `make lint` | `golangci-lint run ./...` | 静态检查和 depguard 分层检查 |
| `make test` | `go test -race -count=1 ./...` | 全包 race 测试；包含会启动 PostgreSQL 的路由与契约测试 |
| `make openapi` | `go run ./cmd/openapi -check -output api/openapi.json` | 路由覆盖、类型绑定、错误码和生成物漂移门禁 |

测试使用 `-count=1`，避免 Go test cache 隐藏数据库、并发或生成物问题。

## 2. 测试层级

### 2.1 单元测试

纯函数和无外部依赖组件使用 Go `testing` 与 testify，覆盖：

- 统一信封和错误映射；
- 字段错误码与业务错误码；
- 时间、金额、分页和密码工具；
- JWT 签发与解析；
- OpenAPI 和错误码解析生成器；
- 配置解析与启动校验；
- 外部供应商适配器的固定测试向量。

### 2.2 Router 与业务集成测试

`internal/router/*_integration_test.go` 使用真实 PostgreSQL 18，并通过完整 router、handler、service 和 repository 链验证：

- UoW 提交、回滚和 panic；
- 认证与会话撤销；
- 帖子、评论、通知、搜索、上传和管理端行为；
- 版本快照、软删除、恢复和审核；
- 计数器触发器与图片引用锁；
- 并发幂等和查询预算。

这些测试是普通 `go test ./...` 的一部分，不需要额外 build tag。

### 2.3 HTTP 契约测试

单独运行：

```bash
make test-contract
```

契约套件通过 Hertz test transport 请求实际路由，不直接调用 handler、service 或 repository。它遍历运行时路由表并验证：

- 每条业务与 runtime 路由都被命中；
- 成功响应和错误响应的统一信封；
- 401、403、404、405、422、500 的稳定结构；
- 422 字段错误数组；
- 500 `error_id`；
- `/config` 的关键列表；
- 认证成功响应的 token。

路由集合来自共享的 `internal/apicontract` 注册表，不维护第二份手写端点清单。

### 2.4 收敛门禁

单独运行：

```bash
make test-convergence
```

该目标依次执行：

1. HTTP 契约套件；
2. PostgreSQL 热路径 SELECT 查询预算；
3. 路由与契约端点集合对账。

查询预算关注 SELECT 数是否随 `page_size` 增长，而不是在不固定数据集的情况下比较耗时。没有证据时不要为了“优化”盲目增加索引。

### 2.5 `integration` tag 目标

```bash
make test-integration
```

该目标的精确范围是：

```bash
go test -race -count=1 -tags=integration ./test/...
```

它只覆盖 `test/` 树；当前 `internal/router` 的数据库集成测试由 `make test` 运行，不包含在这个目标中。新增带 `integration` tag 的 `test/` 套件时应保持这一边界清晰。

## 3. Schema 回归

运行：

```bash
make schema-test
```

脚本使用独立的 `postgres:18` 容器执行三条链路：

1. 数据链路：干净库 → 执行全部 Up → 运行 `schema_smoke.sql`；
2. 结构链路：另一干净库 → Up → Down → Up；
3. 可失败性：故意把“34 张业务表”的断言改错为 999，必须非零退出。

数据链路和结构链路不能复用同一份已跑 smoke 的数据库。Smoke 会留下受外键 `RESTRICT` 保护的审核/提议数据，在该库上执行 Down 正确失败；混用会把有效约束误报成迁移不可回滚。

Schema 变更必须补充：

- 至少一个合法正例；
- 至少一个应被数据库拒绝的负例；
- 状态恢复或重复动作的回归行为；
- 结构数量或对象存在性断言；
- 能证明新断言真正可失败的测试。

## 4. OpenAPI 验证

更新和检查：

```bash
make openapi-generate
make openapi
```

生成器检查三类集合：

- Hertz 运行时路由；
- route/status/auth 注册；
- 请求/响应类型绑定。

错误码从 `internal/apierr/codes.go` 的 Go AST 直接读取。最后重新生成规范并与提交文件逐字节比较。因此路由漏登记、DTO 漏绑定、错误码变化和过期 JSON 都会失败。

只运行 `make openapi-generate` 不算验收；必须审阅 `api/openapi.json` 差异，再运行 `make openapi`。

## 5. 覆盖率

```bash
make cover
```

该目标对 `./internal/...` 生成 `coverage.out`，然后输出总覆盖率。`internal/router` 包含真实 PostgreSQL 测试，因此仍然需要 Docker。

覆盖率用于发现未覆盖区域，不能替代契约、并发和数据库不变式测试。关键负例即使不显著提高行覆盖率也必须保留。

## 6. Compose 运行时验证

从仓库根目录执行：

```bash
DANSHI_POSTGRES_PORT=55432 DANSHI_SERVER_PORT=18000 \
  docker compose -f deploy/compose/docker-compose.yml up -d --build --wait server
curl -fsS http://127.0.0.1:18000/health
curl -fsS http://127.0.0.1:18000/ready
curl -sS http://127.0.0.1:18000/api/v2/nope
DANSHI_POSTGRES_PORT=55432 DANSHI_SERVER_PORT=18000 \
  docker compose -f deploy/compose/docker-compose.yml down -v
```

Compose 通过 `service_completed_successfully` 保证迁移容器先成功结束，server 才启动。预期：

- `/health` 返回 `healthy`；
- `/ready` 返回 `ready`；
- 未知业务路径返回 JSON 统一错误体，而不是纯文本；
- migrate 失败时 server 不应启动。

## 7. 隔离与数据安全

- 测试只使用 testcontainers 或明确的临时 Compose project。
- 不要把测试 `DATABASE_URL` 指向开发库、长期测试库或生产库。
- 不要复用已有 PostgreSQL 容器的 volume 运行 schema smoke。
- 测试结束应清理临时容器和卷；失败时可先保留日志，但不得把用户数据写入 artifact。
- 生产环境禁止执行冒烟写入。

## 8. 常见问题

### Docker 不可用

如果 `make test` 在 testcontainers 初始化阶段失败，先验证：

```bash
docker version
docker run --rm postgres:18 postgres --version
```

不要通过跳过数据库测试来宣告门禁通过。

### 端口冲突

testcontainers 不依赖固定宿主机端口。Compose 端口可用 `DANSHI_POSTGRES_PORT` 和 `DANSHI_SERVER_PORT` 覆盖。

### OpenAPI 漂移

先运行 `make openapi-generate`，审阅差异是否来自真实路由、DTO 或错误码改动。不要直接手改 `api/openapi.json` 迎合门禁。

### Schema smoke 只打印失败但退出码为 0

这是无效测试。断言必须使用会 `RAISE EXCEPTION` 的检查，并由可失败性链路证明错误值会导致非零退出。
