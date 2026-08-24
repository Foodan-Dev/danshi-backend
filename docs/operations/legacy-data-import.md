# 旧库一次性迁入 Runbook

本文档取代归档的 `docs/history/data-migration-plan.md` 作为本轮实际执行手册。工具只服务于本地隔离副本演练；不包含生产连接、切流、部署或生产回退操作。

## 安全边界

- 来源和目标 DSN 都必须使用 `localhost` 或 loopback IP。命令会在建立连接前拒绝其他主机，因此不能误连生产数据库。
- 来源的全部查询位于一个 `REPEATABLE READ READ ONLY` 事务；命令还会读取 `transaction_read_only` 并要求值为 `on`。
- 目标 goose 版本必须与当前构建的 `db.ExpectedVersion` 完全相等。命令读取该常量，不另写版本数字。
- 首次导入要求所有业务表为空，字典 seed 和 `goose_db_version` 除外。目标非空时，只有它已经逐行等于本次完整导入结果才允许幂等重跑；部分数据或其他业务数据会以 `target_not_empty_or_imported` 拒绝。
- 导入在一个 `SERIALIZABLE` 事务中完成，并持有事务级 advisory lock。任一步失败都会回滚整个目标事务。
- 错误与对账输出只包含表名、来源 UUID、目标字段名、固定 code 和计数，不包含邮箱、密码哈希、昵称、简介、标题、正文、通知预览、对象键或 URL 原文。
- 不把 dump、DSN、密码或其他生产数据复制到仓库。

## 映射与幂等

旧 UUID 先规范化，再计算：

```text
SHA-256("danshi-legacy-import/v1\0uuid\0" + canonical_uuid)
```

取摘要前 64 位、清除符号位，得到正的 `bigint`。文本标签使用独立的 `tag` 命名空间和小写名称计算 ID。每张目标表在写入前检查映射碰撞；碰撞会终止，不做猜测。

实体按确定 ID `INSERT ... ON CONFLICT`，关系表按复合主键 upsert。只因历史数据存在的停用菜系与口味也使用独立命名空间的确定 ID 幂等写入，不修改 goose seed。不可修改的角色/封禁审计表使用确定 ID 和 `ON CONFLICT DO NOTHING`；非空目标只有先通过完整 verify 才能进入重跑，所以不会用 `DO NOTHING` 掩盖不同内容。identity sequence 保持从小值开始，后续正常写入无需跳到散列 ID 的最大值。

## 迁入口径

- `companion` 合并为 `seeking`；食堂按 seed 名称匹配；标签按原名创建。
- 菜系中 `西餐` 映射到 seed `西式`，`快餐` 映射到 seed `其他`；`云南菜`、`台湾菜`、`江西菜` 原名创建为 `is_active=false` 的历史词条，`sort_order` 排在 seed 之后。
- 分享帖的 `flavors` 写入 `post_flavors(stance='has')`：`咸`、`辣`、`酸甜` 原名创建为 `is_active=false` 的历史词条。求推荐帖的 `preferences.prefer_flavors` 写入 `stance='prefer'`，`preferences.avoid_flavors` 写入 `stance='avoid'`；`清淡`、`麻辣` 命中同名 seed，`重辣` 映射到 seed `特辣`。
- `flavors` 只有 JSONB array 才展开，`preferences` 只有 JSONB object 才展开；SQL NULL 与 JSON `null` 标量都保持无值，不伪装成空容器。`post_flavors` 以 `(post_id, flavor_id)` 去重；重复的同 stance 映射合并，冲突 stance 则终止导入。
- `budget_range` 拍平，`price` 和 `view_count` 原值迁入；触发器维护的计数列不从旧值写入。
- 评论保留真实父链并重新计算 `root_id`。根评论回复对象填帖子作者；两条可唯一推定的错挂回复重挂到同楼较早评论；指定异常评论改为回复父评论作者。
- 2 个空 URL/pending 资产、8 个占位图引用和 3 个假头像被剔除。其余 38 张存量资产按 grandfather 语义迁为 `ready/pass`；真实命中的帖子图片和头像建立外键关系。
- 13 条悬空点赞和 2 条悬空通知丢弃；关注、收藏直迁。
- 旧 `admin` 只授予现有 `moderator`，旧 `super_admin` 授予现有 `super_admin`。普通用户不写 `user_roles`。
- `is_active=false` 映射为永久封禁，固定理由为“旧系统账号已停用”；旧系统没有操作者，因此 `banned_by` 和审计 `actor_id` 如实留空。
- 旧库另有 11 个非空 `hometown`，新 schema 没有目标字段，明确丢弃，不拼进 `bio`。
- 旧 `flavors` 的 16 个 seed 名称和启用状态逐行与新 seed 核对；目标 goose seed 的排序为真源，不迁旧 `sort_order`。

勘察数字有一处与原决策表不同：38 张可迁资产中，实际是 27 张 post 资产被帖子引用、2 张 post 资产 ready 但未引用、9 张 avatar 资产被头像引用；不是“4 张 post 资产未引用”。执行以真实 URL 引用关系为准，不人为解除两条有效引用。

## 演练步骤

先准备两个只监听本机的隔离 PostgreSQL：source 从受控 dump 恢复，target 是刚执行完当前 goose migrations 的空业务库。不要在 shell 历史或文档中填写真实密码。

```bash
export DANSHI_LEGACY_SOURCE_URL='<loopback source DSN>'
export DATABASE_URL='<loopback empty target DSN>'
```

确认当前代码与数据库版本，再执行第一轮：

```bash
go run ./cmd/danshi-legacy-import import | tee /tmp/danshi-legacy-import-1.txt
go run ./cmd/danshi-legacy-import verify | tee /tmp/danshi-legacy-verify-1.txt
```

验证幂等：

```bash
go run ./cmd/danshi-legacy-import import | tee /tmp/danshi-legacy-import-2.txt
go run ./cmd/danshi-legacy-import verify | tee /tmp/danshi-legacy-verify-2.txt
cmp /tmp/danshi-legacy-import-1.txt /tmp/danshi-legacy-import-2.txt
cmp /tmp/danshi-legacy-verify-1.txt /tmp/danshi-legacy-verify-2.txt
```

基于 dump 的自动演练会自行启动并清理 source/target 两个容器：

```bash
export DANSHI_LEGACY_DUMP='<受控 dump 的仓库外路径>'
go test -count=1 -run '^TestImportAndVerifyFromDump$' ./internal/legacyimporter
```

`DANSHI_LEGACY_DUMP` 未设置时该测试 skip，不会使普通开发或 CI 失败。

## Verify 覆盖与判定

成功必须以 `VERIFY_OK mismatches=0` 结束。Verify 会：

1. 从同一个只读来源快照重新构造全部确定映射和负责人裁决。
2. 对 users、角色/封禁绑定及审计、图片、posts、tags 及全部帖子关系、comments、follows、favorites、拆分后的两张 likes、notifications 逐行逐字段比较。
3. 对源 `flavors` seed 逐行核对名称与启用状态，对 3 个历史菜系和 3 个历史口味逐字段核对确定 ID、原名、停用状态、排序与时间戳，并确认本次不应写入的 mentions、histories、moderation、sessions、verification codes 和 dictionary suggestions 仍为空。
4. 对每条评论核对 post/author/parent/root/reply target，对每条动作关系核对两端，对每个图片引用核对资产与位置。
5. 将帖子和评论的 like/favorite/comment/reply 计数与动作表、可见评论重新派生的值逐行比较。Import 结束前还会调用 `danshi_recount_all()`。
6. 逐行输出所有故意丢弃或改写的来源 UUID；出现差额时输出 `MISMATCH table=... source_id=... field=... code=...`，但不输出字段值。

任意 `MISMATCH`、来源决策计数变化、缺失 seed、食堂未命中、评论父节点推定不唯一、ID 碰撞、目标多余行或 goose 版本不符都判失败。

## 失败与回退

- `import` 失败：目标事务已经整体回滚，保留错误 code 和安全计数，修复原因后重跑。不要手工补半张表。
- `verify` 失败：目标不可信，不进入后续流程。根据 `table/source_id/field/code` 修复导入器或重新确认裁决；不得直接把目标值改到“看起来相等”。
- 演练目标需要从头开始时，停止并删除明确命名的 disposable target 容器，再新建目标并重跑 goose。来源容器和 dump 始终只读、不得清空或覆盖。
- 本轮没有生产写入、切流或部署，因此不存在生产数据回滚步骤。任何未来生产执行都必须另行评审连接授权、备份、停写窗口和切流方案，不能把本 runbook 的 loopback 限制临时绕过。

## 仓库验收

```bash
make fmt
make lint
make test
make openapi
make schema-test
```

演练完成后同时保留两轮安全输出和 `cmp` 结果到本地 `.codex/reports/`，该目录被 Git 忽略，避免把一次性执行日志混入稳定文档。
