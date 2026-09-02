# 旦食开发指南

本目录保存基于仓库实际结构整理的开发规范。所有代码、数据库 schema、HTTP 契约、配置和
测试改动前，先完整阅读 [AGENTS.md](AGENTS.md)。

## 项目地图

| 主题 | 真源 / 入口 |
|---|---|
| 产品语义 | [`docs/product-requirements.md`](../docs/product-requirements.md) |
| 架构与分层 | [`docs/architecture.md`](../docs/architecture.md) |
| 数据库 schema | [`migrations/*.sql`](../migrations/)；禁止 GORM AutoMigrate |
| schema 行为 | [`migrations/testdata/schema_smoke.sql`](../migrations/testdata/schema_smoke.sql) |
| HTTP 契约 | [`api/openapi.json`](../api/openapi.json)，由代码生成，禁止手改 |
| 路由与契约登记 | [`internal/router`](../internal/router)、[`internal/apicontract`](../internal/apicontract) |
| HTTP DTO 与 handler | [`internal/handler`](../internal/handler) |
| 业务编排 | [`internal/service`](../internal/service) |
| 数据访问 | [`internal/repository`](../internal/repository) |
| 运行配置 | [`internal/config`](../internal/config) 与 [`README.md`](../README.md) |
| 测试与门禁 | [`docs/testing.md`](../docs/testing.md)、[`.github/workflows/ci.yml`](../.github/workflows/ci.yml) |

## 修改流程

1. 先确认产品语义和既有架构，不以代码猜测未决定的规则。
2. 需要持久化变更时新增顺序编号 migration，绝不回改已发布 migration。
3. 新增或修改 HTTP 端点时，同时更新运行时路由、`internal/apicontract` 声明、handler DTO 和测试；
   然后用 `make openapi-generate` 更新生成物。
4. 将业务语义、事务编排、并发协议放在 service；handler 只负责 HTTP 输入输出，repository 只负责查询。
5. 为成功、校验失败、权限/认证失败、并发或唯一性冲突等实际分支补充真实 PostgreSQL 集成测试。
6. 提交前至少运行相关测试；完整验收按下列命令执行。

## 验收命令

```bash
make fmt
make lint
make schema-test
make test
make openapi
```

`make test` 与 `make schema-test` 依赖 Docker。完整说明见
[`docs/testing.md`](../docs/testing.md)。
