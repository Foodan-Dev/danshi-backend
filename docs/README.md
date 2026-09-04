# 旦食后端文档

本目录是旦食后端稳定文档的唯一真源。代码、数据库约束与文档发生冲突时，应先确认产品或设计决策是否需要更新，再让文档与实现同步收敛。

## 产品与设计

| 文档 | 内容 | 适用读者 |
|---|---|---|
| [产品需求](product-requirements.md) | 产品边界、角色、内容模型、审核和非功能要求 | 产品、前端、后端、测试 |
| [架构与设计决策](architecture.md) | 技术栈、分层、schema、服务层协议、错误和发布门禁 | 后端、SRE、评审者 |
| [ADR 0001：Schema ownership](adr/0001-schema-ownership.md) | 为什么 goose SQL 是数据库结构唯一真源 | 后端、数据库维护者 |
| [内容主流程](assets/content-flow.mmd) | 发帖、审核、编辑、软删除与词条提议的 Mermaid 源文件 | 产品、设计、开发 |
| [2026-09-02 name 身份化与密码找回需求（草案）](requirements/2026-09-02-name-and-password-recovery.md) | `name` 身份化、登录与密码找回的待评审需求 | 产品、前端、后端、测试 |

## 开发与运维

| 文档 | 内容 |
|---|---|
| [测试指南](testing.md) | 本地门禁、testcontainers、schema 和运行时验证 |
| [图片上传与资产生命周期](image-hosting.md) | COS 预签名直传、Content-MD5、锁序与退役 |
| [旧库一次性迁入 Runbook](operations/legacy-data-import.md) | 隔离演练、逐行对账、幂等重跑与失败回退 |
| [根目录 README](../README.md) | 快速开始、完整配置表、Make 目标、项目结构和许可证状态 |
| [OpenAPI](../api/openapi.json) | 由运行时路由与 Go 类型生成的接口规范 |

## 历史专项

历史专项不属于日常开发流程，单独放在 `history/`：

| 文档 | 状态 |
|---|---|
| [一次性历史数据迁入方案](history/data-migration-plan.md) | 归档设计；执行前必须重新勘察来源数据并改写为经评审的 runbook |

## 事实来源优先级

不同类型的事实分别有可执行真源：

1. 产品语义：`docs/product-requirements.md`。
2. HTTP wire contract：`api/openapi.json`，由代码生成并受漂移门禁保护。
3. 数据库结构：`migrations/*.sql`，不使用 GORM AutoMigrate。
4. Schema 行为：`migrations/testdata/schema_smoke.sql`。
5. 运行配置：`internal/config` 中的 `bindings`。
6. 稳定架构决策：本文档索引下的架构文档与 ADR。

进度、临时排查记录和一次性执行日志不应写进稳定文档。
