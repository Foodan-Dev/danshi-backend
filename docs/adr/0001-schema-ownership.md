# ADR 0001：数据库 schema ownership

- 状态：Accepted
- 日期：2026-08-22

## 背景

旦食的数据库包含复合外键、部分唯一索引、生成列、状态 CHECK、计数同步触发器、追加不可篡改触发器、软删除防线和事务局部运维开关。这些结构无法由运行时 ORM 模型完整、稳定地表达。

如果应用启动、测试夹具、ORM AutoMigrate 和运维脚本分别创建或推断 schema，数据库结果将依赖执行路径，约束也会在副本之间漂移。

## 决定

`migrations/*.sql` 中的 goose migration 是数据库 schema 的唯一真源。

- 应用不调用 GORM AutoMigrate。
- 测试不维护独立建表 SQL；测试数据库执行正式 migrations。
- migration SQL 通过 `go:embed` 编入 `danshi-migrate`。
- `goose_db_version` 是数据库运行版本真源。
- `danshi-server` 启动时读取当前版本，并与二进制内嵌期望版本严格比较；不相等就拒绝启动。
- 部署先运行独立 migrate job/container，成功后再启动或滚动 server。
- 常规开发允许单步 Down 用于演练；生产变更优先通过新的 forward migration 修正，不改写已经发布的 migration。
- 所有新约束都必须在 `migrations/testdata/schema_smoke.sql` 中有可失败的回归断言。

## 迁移验证

Schema CI 使用两个独立干净数据库：

1. Up → smoke，验证数据行为、约束、函数和触发器；
2. Up → Down → Up，验证结构可回滚和可重建。

另有一条可失败性测试，故意修改断言的期望值并要求非零退出。只打印“期望/实际”但不影响退出码的 SQL 不构成测试。

## 备选方案

### 在应用启动时 AutoMigrate

拒绝。它让服务副本拥有隐式 DDL 权限，无法表达全部 PostgreSQL 约束，并使发布顺序不可控。

### 让测试夹具手写简化 schema

拒绝。简化结构无法验证真实触发器、复合外键和事务开关，测试通过也不能说明生产 migration 可用。

### 维护额外 schema JSON 或 fingerprint 作为结构真源

拒绝。静态副本会漂移，不能替代可执行 migration。OpenAPI 只描述 HTTP，不承担数据库结构职责。

### Server 自动执行 Up 后再监听

拒绝。多副本滚动时会混合迁移和服务生命周期；迁移失败、长事务或锁等待难以隔离。独立 migrate 进程能让编排器明确表达依赖。

## 后果

正面后果：

- 开发、测试、CI 和部署执行同一组 SQL；
- schema 变更可审计、可排序、可回归；
- server 不会静默连接不兼容数据库；
- distroless migrate 镜像无需额外复制 SQL 文件。

代价与约束：

- 开发者必须同时维护 Up、Down 和 smoke 断言；
- model 变化不自动生成 migration；
- 修改已发布 migration 会破坏历史可重复性，因此应新增 migration；
- 运行编排必须提供独立 migrate 步骤；
- 一次性历史数据导入不能伪装成常规 schema migration，应使用独立、经评审的 runbook。
