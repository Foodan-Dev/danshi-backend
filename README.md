# danshi_backend_go

旦食（Danshi）后端的 Go 重写版。复旦校园美食分享平台。

- **产品需求唯一真源**：`../Danshi_backend/docs/product-requirements.md`
- **技术方案**：`../Danshi_backend/docs/go-rewrite-plan.md`
- **数据迁移**：`../Danshi_backend/docs/data-migration-plan.md`

实现与产品需求文档冲突时以文档为准；文档有误先改文档再改实现。

## 快速开始

```bash
make help          # 看所有命令
make test          # 单元测试
make schema-test   # schema 回归（需要 Docker）
make build         # 构建两个二进制
```

本地起全栈：

```bash
cd deploy/compose && docker compose up --build
```

## 必须知道的几条

**路由前缀是 `/api/v2`。** `/api/v1` 由旧 Python 服务继续提供，网关按路径分流。

**schema 的唯一真源是 `migrations/00001_init.sql`，不用 GORM AutoMigrate。**
整个设计建立在触发器与 CHECK 约束上，被 AutoMigrate 改掉一处防线就全线失守。

**server 启动时会核对 schema 版本**，不符直接拒绝启动。迁移由独立的
`danshi-migrate` 镜像执行，不要混进 server。

**数据库层有多道防线，写代码时会撞上，这是有意的：**

| 现象 | 原因 |
|---|---|
| 直接 `UPDATE posts SET like_count=...` 被拒 | 计数列由触发器维护。重算用 `danshi_recount_all()` |
| `DELETE FROM users/posts/comments/tags` 被拒 | 一律软删除。运维清除需事务内 `SET LOCAL danshi.allow_hard_delete='on'` |
| `UPDATE moderation_records` / 历史表被拒 | 审核流水与编辑历史追加不可篡改。人工复核要新插一行并 `supersedes_id` 指回 |
| 帖子编辑后没写版本行，审核记录插不进去 | 审核记录必须锚定被审版本，历史表是全量版本表 |

**更新时间的语义是「内容被编辑」。** 浏览数自增等非内容变更必须用 GORM 的
`UpdateColumn`，用 `Update`/`Updates` 会被 `autoUpdateTime` 记成编辑。

## 分层纪律

```
router → handler → service → repository → model
```

由 `.golangci.yml` 的 `depguard` 强制：handler 不得 import GORM 或 repository，
service 不得 import hertz，repository/model 不得反向依赖上层。

repository 一律通过 `db.FromContext(ctx)` 取事务句柄，**不接受注入 `*gorm.DB`**——
那会绕开请求级事务，出现一半提交一半没提交的写入。
