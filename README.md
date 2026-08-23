# 旦食后端

旦食后端是复旦校园美食分享平台的服务端。它为移动端提供账号与会话、校园餐厅与窗口词表、帖子、评论、搜索、互动、图片直传、内容审核和管理端 API。

服务使用 Go 编写，HTTP API 统一挂载在 `/api/v2`；`/health`、`/ready` 和 `/metrics` 是不带版本前缀的运行时端点。API 响应采用统一信封，数据库约束、事务边界和契约测试共同保护内容版本、审核流水、计数器与图片引用的一致性。

## 技术栈与架构

- Go 1.26.7
- CloudWeGo Hertz 0.10
- GORM 2 + pgx 5
- PostgreSQL 18
- goose SQL migrations
- Viper 环境变量配置
- testcontainers-go + PostgreSQL 18 集成测试
- kin-openapi 驱动的仓库内 OpenAPI 生成器
- 腾讯云 COS、SES 与数据万象适配

请求沿固定方向流动：

```text
router → handler → service → repository → model
```

`.golangci.yml` 中的 `depguard` 强制这一依赖方向。每个业务请求由 UoW 中间件建立事务，repository 通过请求上下文取得当前事务句柄，不能绕开事务自行持有数据库连接。详细设计见[架构与设计决策](docs/architecture.md)。

## 快速开始

### 环境要求

- Go 1.26.7
- Docker Engine 和 Docker Compose
- golangci-lint 2.13.1（仅 `make lint` 需要）

以下命令都从仓库根目录执行。

### 使用 Docker Compose 启动完整服务

该命令启动 PostgreSQL 18，先运行一次性迁移容器，再启动 HTTP 服务。示例使用高位宿主机端口，避免占用本机常见的 5432 和 8000 端口。

```bash
DANSHI_POSTGRES_PORT=55432 DANSHI_SERVER_PORT=18000 \
  docker compose -f deploy/compose/docker-compose.yml up -d --build --wait server
```

验证存活、数据库就绪和迁移状态：

```bash
curl -fsS http://127.0.0.1:18000/health
curl -fsS http://127.0.0.1:18000/ready
DANSHI_POSTGRES_PORT=55432 DANSHI_SERVER_PORT=18000 \
  docker compose -f deploy/compose/docker-compose.yml run --rm migrate -cmd status
```

停止服务并删除本地开发数据卷：

```bash
DANSHI_POSTGRES_PORT=55432 DANSHI_SERVER_PORT=18000 \
  docker compose -f deploy/compose/docker-compose.yml down -v
```

### 使用本机 Go 工具链启动

先只启动 PostgreSQL：

```bash
DANSHI_POSTGRES_PORT=55432 \
  docker compose -f deploy/compose/docker-compose.yml up -d --wait postgres
```

为本地开发设置最小配置。这里的密钥仅用于本地数据库和本地进程，不可用于部署环境：

```bash
export APP_PROFILE=dev
export PORT=18001
export DATABASE_URL='postgres://postgres:danshi@127.0.0.1:55432/danshi?sslmode=disable&TimeZone=UTC'
export JWT_SECRET_KEY='local-dev-jwt-secret-0123456789abcdef'
export EMAIL_VERIFICATION_SECRET='local-dev-email-secret-0123456789ab'
```

运行迁移，然后启动服务：

```bash
go run ./cmd/danshi-migrate -cmd up
go run ./cmd/danshi-server
```

服务启动后可从另一个终端验证：

```bash
curl -fsS http://127.0.0.1:18001/health
curl -fsS http://127.0.0.1:18001/ready
```

结束服务后清理数据库：

```bash
DANSHI_POSTGRES_PORT=55432 \
  docker compose -f deploy/compose/docker-compose.yml down -v
```

### 运行测试

完整测试会通过 testcontainers 启动真实 PostgreSQL 18，因此 Docker daemon 必须可用：

```bash
make test
make schema-test
```

测试分层、隔离方式和故障排查见[测试指南](docs/testing.md)。

## 配置

`internal/config` 中的 `bindings` 是环境变量的唯一清单。配置在进程启动时一次性加载并校验；配置非法时服务拒绝启动。`DATABASE_URL` 必须包含 `TimeZone=UTC`。

### 基础与数据库

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `APP_PROFILE` | `dev` | 运行环境，只接受 `dev` 或 `prod` |
| `PORT` | `8000` | HTTP 监听端口 |
| `DATABASE_URL` | 无 | PostgreSQL URL，必填，必须指定数据库名和 `TimeZone=UTC` |
| `DB_MAX_OPEN_CONNS` | `20` | 最大打开连接数，必须为正数 |
| `DB_MAX_IDLE_CONNS` | `10` | 最大空闲连接数，不得超过最大打开连接数 |
| `DB_CONN_MAX_LIFETIME_SECONDS` | `1800` | 连接最长复用时间，单位为秒 |

### 认证、注册与 CORS

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `JWT_SECRET_KEY` | 无 | JWT HMAC 密钥，必填，至少 32 字节且不能使用常见占位值 |
| `JWT_EXPIRE_MINUTES` | `60` | access token 有效期 |
| `JWT_REFRESH_EXPIRE_DAYS` | `30` | refresh token 和会话有效期 |
| `EMAIL_VERIFICATION_REQUIRED` | `true` | 是否要求注册验证码；生产环境必须为 `true` |
| `EMAIL_VERIFICATION_SECRET` | 无 | 验证码摘要密钥；启用邮箱验证时必填，至少 32 字节 |
| `ALLOWED_EMAIL_DOMAINS` | `fdueat.com,m.fudan.edu.cn,fudan.edu.cn` | 允许注册的邮箱域名，逗号分隔 |
| `CORS_ALLOW_ORIGINS` | 空 | 允许的来源，逗号分隔；生产环境必须显式配置 |
| `CORS_ALLOW_CREDENTIALS` | `false` | 是否允许携带凭据；为 `true` 时来源不能包含 `*` |

### 腾讯云 SES、COS 与内容审核

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `TENCENT_CLOUD_SECRET_ID` | 无 | 腾讯云访问标识；生产环境必填 |
| `TENCENT_CLOUD_SECRET_KEY` | 无 | 腾讯云访问密钥；生产环境必填 |
| `TENCENT_CLOUD_REGION` | `ap-guangzhou` | SES 区域 |
| `TENCENT_SES_FROM_EMAIL` | `no-reply@danshi.fdueat.com` | SES 发件邮箱 |
| `TENCENT_SES_FROM_NAME` | `旦食` | SES 发件人名称 |
| `TENCENT_SES_TEMPLATE_ID` | `0` | SES 模板 ID；生产环境启用邮箱验证时必须为正数 |
| `COS_BUCKET` | 无 | COS bucket 名称；生产环境必填 |
| `COS_REGION` | `ap-shanghai` | COS 区域 |
| `COS_IMG_DOMAIN` | 无 | 图片公开域名，非空时必须是 HTTPS URL；生产环境必填 |
| `COS_MAX_IMAGE_BYTES` | `10485760` | 单张图片上限，默认 10 MiB |
| `COS_PRESIGN_TTL_SECONDS` | `600` | 预签名上传 URL 有效期 |
| `COS_PRESIGN_GET_TTL_SECONDS` | `3600` | 私有图片预签名读取 URL 有效期；默认 1 小时 |
| `TENCENT_CI_BIZ_TYPE` | 空 | 数据万象审核策略标识 |
| `TENCENT_CI_CALLBACK_URL` | 无 | 数据万象回调 HTTPS URL；生产环境必填 |
| `MODERATION_CALLBACK_TOKEN` | 无 | 回调鉴权密钥；生产环境必填且至少 32 字节 |
| `FEISHU_MODERATION_WEBHOOK_URL` | 无 | 可选的审核告警 HTTPS webhook |

### 日志与遥测

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `OTLP_ENDPOINT` | 空 | OTLP/HTTP Collector traces 地址；支持 `host:port`（明文，默认 `/v1/traces`）或完整 `http(s)` URL，为空时完全禁用 tracing 且不创建连接 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error`；生产环境输出 JSON 日志 |

Prometheus 直接拉取 `GET /metrics`，不经过 Collector。该端点不鉴权且不进入请求级数据库事务；生产环境应在网关或网络策略层限制抓取来源。HTTP 指标和 server span 的路由维度使用 `/api/v2/posts/:post_id` 这类模板，不记录实际资源 ID。启用 tracing 后，请求日志会同时包含 `request_id`、`trace_id` 和 `span_id`；数据库 span 不记录 SQL 文本及变量值。

生产部署应通过密钥管理系统注入敏感值，不要提交 `.env`、token 或云访问密钥。

## API 文档

提交到仓库的 [api/openapi.json](api/openapi.json) 是 OpenAPI 3.0.3 文档。它由真实 Hertz 路由、请求/响应 Go 类型和错误码清单生成，不是手写接口副本。

- Apifox：选择“导入项目 / OpenAPI”，导入 `api/openapi.json`。
- Swagger UI：可用官方镜像在本地查看：

```bash
docker run --rm -d --name danshi-swagger-ui \
  -p 18088:8080 \
  -e SWAGGER_JSON=/spec/openapi.json \
  -v "$PWD/api:/spec:ro" \
  swaggerapi/swagger-ui
curl -fsS --retry 30 --retry-delay 1 --retry-connrefused --retry-all-errors \
  http://127.0.0.1:18088/ >/dev/null
docker stop danshi-swagger-ui
```

修改路由、DTO 或错误码后先更新生成物，再检查漂移：

```bash
make openapi-generate
make openapi
```

生成链同时执行三道门禁：运行时路由与契约注册表对账、运行时路由与类型绑定对账、重新生成结果与已提交 JSON 逐字节对账。任何路由漏登记、类型漏绑定或错误码漂移都会失败。

## 开发工作流

`make help` 会显示日常目标。当前 Make 目标如下：

| 命令 | 用途 | Docker |
|---|---|---|
| `make help` | 显示带说明的目标 | 否 |
| `make tidy` | 整理 `go.mod` / `go.sum` | 否 |
| `make fmt` | 对全部 Go 文件运行 gofmt | 否 |
| `make lint` | 运行 golangci-lint，包括分层依赖检查 | 否 |
| `make test` | 在 race 模式下运行全部包；部分测试启动 PostgreSQL | 是 |
| `make test-contract` | 运行 HTTP 黑盒契约套件 | 是 |
| `make test-integration` | 对 `test/...` 运行 integration tag 套件 | 是 |
| `make test-convergence` | 运行契约、查询预算和端点对账门禁 | 是 |
| `make openapi` | 检查 OpenAPI 覆盖、错误码和文件漂移 | 否 |
| `make openapi-generate` | 重新生成 `api/openapi.json` | 否 |
| `make cover` | 生成内部包覆盖率并输出总覆盖率 | 是 |
| `make build` | 构建 server、migrate 和 jobs 三个二进制 | 否 |
| `make build-server` | 只构建 `bin/danshi-server` | 否 |
| `make build-migrate` | 只构建 `bin/danshi-migrate` | 否 |
| `make build-jobs` | 只构建 `bin/danshi-jobs` | 否 |
| `make docker` | 构建 server、migrate 和 jobs 三个镜像 | 是 |
| `make schema-test` | 在隔离 PostgreSQL 18 中验证 up、down、约束与断言可失败性 | 是 |
| `make clean` | 删除 `bin/` 和 `coverage.out` | 否 |

提交前至少运行：

```bash
make fmt && make lint && make test && make openapi
```

如果修改 schema，还必须运行 `make schema-test`；如果修改 HTTP 契约，先运行 `make openapi-generate`，并审阅 `api/openapi.json` 的差异。

## 数据库迁移

`migrations/*.sql` 是 schema 的唯一真源，服务不使用 GORM AutoMigrate。迁移 SQL 通过 `go:embed` 编入 `danshi-migrate`；server 启动时会核对 goose 版本，不一致就拒绝提供服务。

新增迁移时：

1. 按递增的五位版本号创建文件，例如 `migrations/00003_add_example.sql`。
2. 文件同时提供 `-- +goose Up` 和可验证的 `-- +goose Down`。
3. 为约束行为补充 `migrations/testdata/schema_smoke.sql` 断言。
4. 运行 `make schema-test`，确认干净库 `up → smoke` 与独立干净库 `up → down → up` 都通过。
5. 构建与部署时先运行 migrate，再启动 server；不要让 server 自行迁移。

本地查看迁移版本可使用：

```bash
go run ./cmd/danshi-migrate -cmd status
go run ./cmd/danshi-migrate -cmd version
```

## 项目结构

```text
.
├── api/                    # OpenAPI 生成物与契约资料
├── cmd/
│   ├── danshi-migrate/     # 独立迁移执行器
│   ├── danshi-jobs/        # 外部调度器触发的一次性后台任务
│   ├── danshi-server/      # HTTP 服务
│   └── openapi/            # OpenAPI 生成与漂移检查
├── docs/                   # 产品、架构、开发与运维文档的唯一真源
├── internal/
│   ├── apicontract/        # 路由状态、鉴权与类型绑定契约
│   ├── apierr/             # 业务错误与稳定错误码
│   ├── config/             # 环境变量绑定和启动校验
│   ├── handler/            # HTTP 绑定、校验和响应组装
│   ├── infra/              # 数据库与外部服务适配
│   ├── model/              # GORM 运行时模型
│   ├── openapi/            # 规范生成器
│   ├── repository/         # 持久化访问
│   ├── router/             # 路由、中间件和运行时探针
│   └── service/            # 业务规则与事务内编排
├── migrations/             # goose SQL 与 schema 回归断言
├── test/                   # HTTP 契约与收敛测试
├── deploy/                 # Dockerfile 与本地 Compose
├── Makefile
└── go.mod
```

完整文档索引见 [docs/README.md](docs/README.md)。

## 参与贡献

欢迎提 issue 与 pull request。动手之前请先读[架构与设计决策](docs/architecture.md)与仓库根目录的
[`AGENTS.md`](AGENTS.md)——后者写明了本项目的第一性原理，与它冲突的方案即使更省事也不会被采纳。

### 提交前

```bash
make fmt          # 格式化
make lint         # 静态检查，含分层纪律，必须 0 issues
make test-fast    # 秒级反馈，不起容器
make test         # 全量，含 -race 与 PostgreSQL 集成测试（需要 Docker）
make openapi      # 契约生成与四道门禁
make schema-test  # schema 回归断言
```

`make test-fast` 用于日常迭代，**提交前仍须跑 `make test`**：并发一致性测试依赖 `-race`
与真实数据库，跳过等于没测。

### 提交信息

遵循 [Conventional Commits](https://www.conventionalcommits.org/)，格式与 type 清单见
[`AGENTS.md`](AGENTS.md) 的 Git 规范一节。一次提交只做一件事；正文保持简短，
根因分析与验证过程写在 PR 描述里，不写进每一条提交。

### 契约变更

`api/openapi.json` 由运行时路由表与 Go 类型生成，**不要手改**。改了接口跑
`make openapi-generate` 重新生成并提交。破坏性变更登记在
[`api/BREAKING-CHANGES.md`](api/BREAKING-CHANGES.md)。

四道门禁会拦住常见疏漏：路由未登记、spec 漂移、错误码未同步、GET 端点未声明 query 参数。

### 数据库

`migrations/00001_init.sql` 是 schema 真源。已发布的迁移不要修改，新增变更写新文件并更新
`db.ExpectedVersion`；服务启动时会核对版本，不匹配直接拒绝启动。

## 安全

发现安全问题请**不要**公开提 issue，直接联系维护者。

本项目对以下几类问题特别关注：权限边界绕过、用户内容越权访问、错误响应泄露内部信息、
审核链路可被规避。

## 许可证

本项目采用 [Apache License 2.0](LICENSE)。
