# 一次性历史数据迁入方案

> 分类：历史专项，不属于日常开发文档
>
> 状态：设计约束已归档；执行前必须重新勘察、重新评审并形成独立 runbook
>
> 目标：把既有生产数据库中的有效历史数据清洗、映射并一次性装载到本项目的 PostgreSQL 18 schema。本文不包含可直接执行的凭据、主机路径或生产用户标识。

## 1. 使用边界

本方案只服务一次性历史数据迁入，不用于：

- 日常 goose schema migration；
- 双写或长期数据同步；
- 在线 CDC；
- 常规备份恢复；
- 测试数据生成；
- 用户级合规清除。

来源数据会变化，本文中的历史勘察数字只能帮助评估风险，不能作为执行时对账基线。

## 2. 核心原则

1. 目标 PostgreSQL 实例先执行本仓库全部 migrations，得到空业务库和词表种子。
2. 来源库在一致性快照点之后保持只读，不删除、不清洗原表。
3. 清洗通过“选择性不迁入”和显式映射完成，不修改来源库。
4. 全量 ETL 在单一受控事务和 advisory lock 下执行，结束前完成全部断言。
5. 任一外键映射、枚举、词表或内容关联不明确时 fail closed，不静默置空、取第一条或丢弃。
6. 来源 ID 和目标 ID 的映射作为迁移证据归档，用于抽样、问题追溯和必要的反向映射。
7. 计数器从动作表和关联表重建，不复制来源冗余值。
8. 迁入完成后，先验证数据与运行契约，再允许目标服务写入。

其中 `view_count` 不是动作表可重建的派生计数：它表示旧系统已经发生但没有明细流水的
浏览事实。ETL 应先以 0 插入帖子，再单独把来源 `view_count` 写回；该更新不得修改
`updated_at`，也不需要开启 `danshi.allow_counter_write`。点赞、收藏、评论和回复计数仍然
只能从关系表重建，不能照搬来源值。

## 3. 切换时序

### 3.1 前置条件

- Go 服务完整门禁通过；
- 目标 PostgreSQL 18 实例就绪；
- `migrations/*.sql` 已执行到 server 期望版本；
- 完整 ETL 在恢复副本上连续三次零差异；
- 来源数据清洗名单、词表映射和通知修复规则已经人工批准；
- 备份已完成且实际做过隔离恢复；
- 网关、来源服务、数据库管理员、移动端发布和回滚负责人均在场。

### 3.2 停写不是单条数据库设置

`ALTER DATABASE ... SET default_transaction_read_only = on` 只影响新建立的 session，不会冻结连接池里已经存在的连接。可靠停写需要完整验证：

1. 网关层拒绝来源 API 的 POST、PUT、PATCH、DELETE；GET 继续提供只读浏览。
2. 对来源数据库和应用角色设置默认只读。
3. 排空或重启来源服务连接池，让新 session 继承只读设置。
4. 使用与来源应用相同权限的账号新建连接，分别对用户、帖子和评论执行 INSERT、UPDATE、DELETE 写探针，断言全部失败。
5. 每条写探针使用独立事务或 SAVEPOINT。第一条只读错误会让当前 PostgreSQL 事务进入 aborted，不能让后续语句只验证到“事务已失败”。
6. 不使用超级用户做写探针；超级用户可能绕过只读设置。
7. 查询 `pg_stat_activity`，确认没有残留旧 session 或活跃写事务。
8. 记录停写成立时间和证据，之后才建立来源快照并开始 ETL。

### 3.3 装载与启用

1. 对来源数据执行最终勘察并冻结对账基线。
2. 在目标空库执行全量 ETL。
3. 运行行数、映射、关联、计数、时间戳和抽样断言。
4. 运行 `danshi_recount_all()`，断言没有需要修正的行。
5. 启动 Go 服务，server schema 版本门禁必须通过。
6. 验证 `/health`、`/ready`、登录、信息流、帖子详情、评论、图片和审核链路。
7. 网关启用 `/api/v2`。
8. 发布使用 `/api/v2` 的客户端。
9. 来源系统维持只读观察期，至少四周。

### 3.4 回滚边界

目标服务产生真实写入之前，可以关闭目标路由，并按相反顺序撤销来源只读设置、回收连接池、执行成功写探针后恢复来源写入口。

目标服务产生真实写入之后，来源库不包含这些新数据，已经不存在“只切路由”的无损回滚。此时必须三选一并获得明确授权：

- 把目标新写入反向迁移；
- 接受丢弃目标新写入；
- 保持目标系统并向前修复。

因此最后一次写入型 smoke test 之前必须明确最后无损回滚点。

## 4. 历史勘察摘要

以下数字来自 2026-08-14 的历史快照，只描述当时风险形态：

### 4.1 环境与规模

- 来源数据库：PostgreSQL 16.13；
- 来源代理主键：UUID；
- 目标数据库：PostgreSQL 18；
- 目标代理主键：bigint identity；
- 用户：47；
- 帖子：勘察期间从 31 增至 32，证明未停写时数字会漂移；
- 评论：109；
- 通知：384；
- 关注：44；
- 点赞：勘察期间从 106 增至 107；
- 收藏：0；
- 图片资产：38；
- 邮箱验证码：0。

执行时必须重新查询所有表，并解释与历史数字的每一项差异。

### 4.2 图片数据

历史图片 URL 已批量迁入对象存储，但后续数据仍出现：

- 帖子图片 URL 中 2 个 distinct URL 找不到资产记录，影响 8 篇帖子；
- 用户头像中 3 个非 URL 垃圾值找不到资产记录；
- 图片资产当时包含 9 张 ready 头像、27 张 ready 帖子图片和 2 张 pending 帖子图片。

经过批准的清洗选择后，所有被迁入的帖子图片和头像必须 100% 映射到目标 `image_assets`。未命中不得静默置空；只有清洗决策明确要求置空的头像例外。

### 4.3 JSONB 实际形态

| 来源列 | 历史形态 |
|---|---|
| 帖子标签、口味、图片 | array；未观察到 SQL NULL 或非字符串元素 |
| 评论提及用户 | array；历史快照总元素数为 0 |
| 帖子预算 | 3 行 object，28 行是 JSONB `null` 标量 |
| 帖子偏好 | 2 行 object，29 行是 JSONB `null` 标量 |

JSONB `null` 标量不是 SQL NULL。所有展开前必须先判断：

```sql
jsonb_typeof(value) = 'array'
jsonb_typeof(value) = 'object'
```

不能用 `WHERE value IS NOT NULL` 或简单 `COALESCE(value, '[]')` 代替类型检查；`jsonb_object_keys()` 遇到 JSONB null 会直接报错。

### 4.4 数据质量风险

| 风险 | 历史证据 | 迁入规则 |
|---|---|---|
| 帖子点赞计数漂移 | 31 篇中 16 篇不一致，且全部偏低 | 不复制，按点赞行重建 |
| 评论计数漂移 | 1 篇帖子评论数不一致 | 按评论行重建 |
| 点赞目标无真外键 | 多态裸 ID 可能悬挂 | 解析到帖子或评论后分别写目标表，解析失败不迁 |
| 悬挂通知 | 2 条通知指向不存在帖子 | 不迁 |
| 帖子类型越界 | 1 条记录不属于目标枚举 | 按清洗决策不迁 |
| 词表自由文本 | 可能不命中种子词表 | 逐个 distinct 值人工映射，未决时中止 |

## 5. 数据清洗

### 5.1 禁止启发式删用户

不得按邮箱域名、昵称、是否零产出或字符串包含 `test` 批量判定测试账号。真实管理员或真实用户可能使用非校园邮箱或类似测试的昵称。

清洗只能使用经过人工逐条批准的显式来源 ID manifest。公开仓库不保存真实用户 ID、邮箱或昵称；受控 runbook 应引用受权限保护的 manifest，并记录版本、审批人和校验摘要。

历史清洗决定包含：

- 16 个明确账号不迁；其中部分账号产生的测试内容随之不迁；
- 2 篇由真实管理员产生的冒烟帖子单独不迁；
- 受这些帖子影响的 48 条测试评论不迁；
- 2 个无效头像置空但保留账号；
- 1 个关键运维账号保留并修正邮箱域名。

执行前必须重新检查 manifest 中账号是否产生了新的真实内容。新增关系必须重新决策，不能沿用旧结论自动级联丢弃。

### 5.2 历史清洗后基线

历史估算：

| 对象 | 清洗前 | 不迁 | 预计迁入 |
|---|---:|---:|---:|
| 用户 | 47 | 16 | 31 |
| 帖子 | 32 | 13 | 19 |
| 评论 | 109 | 48 | 61 |
| 点赞 | 107 | 至少 1 | 以解析和去重结果为准 |

这些数值不是执行验收值。最终基线必须从停写后的来源快照重新计算。

## 6. ID 映射与装载拓扑

### 6.1 映射表

每张有代理主键的来源表创建临时映射：

```sql
map_<table>(
    old_id uuid PRIMARY KEY,
    new_id bigint NOT NULL UNIQUE
)
```

所有外键通过映射表翻译。翻译不到时中止，不允许把强引用静默变成 NULL。

点赞等目标使用复合主键的动作表不创建代理 ID 映射。

### 6.2 装载顺序

推荐拓扑：

```text
目标词表种子
  → users
  → image_assets
  → posts
  → comments（按父链拓扑）
  → post_tags / post_flavors / post_images / comment_mentions
  → follows / favorites / post_likes / comment_likes
  → notifications
  → grandfather moderation_records
```

`user_sessions`、`email_verification_codes`、`dictionary_suggestions` 和 `canteen_windows` 在历史快照中没有来源记录，预期为空或只含目标种子/后续运营数据。

### 6.3 推进 sequence

使用 identity 的表如果显式插入 ID，sequence 不会自动跟到最大值。每张表装载后必须推进：

```sql
SELECT setval(
    pg_get_serial_sequence(table_name, 'id'),
    coalesce(max_id, 1),
    max_id IS NOT NULL
);
```

空表使用 `is_called=false`，确保下一次生成 1。执行后用事务内测试插入证明新 ID 大于现有最大值，再回滚测试行。

### 6.4 映射证据

每张映射表行数必须等于对应对象的最终迁入行数。映射表不能与应用业务 schema 混放；应导出到受控归档，保存 schema、生成时间、来源快照标识和校验和。

## 7. 字段与关系转换

### 7.1 用户

| 来源语义 | 目标 | 规则 |
|---|---|---|
| 邮箱 | `users.email` | 小写唯一性预检；批准的运维账号邮箱按显式映射修正 |
| 密码哈希 | `users.password_hash` | 原样保存 bcrypt 哈希 |
| 性别 | `users.gender` | 只允许 `male/female/other`；未知展示值置 NULL 并记录 |
| 头像 URL | `avatar_image_asset_id` | 按公开 URL 找资产并翻译 ID；清洗后必须命中 |
| 活跃标记 | 封禁字段 | 停用账号迁为永久封禁，理由写“迁入前已停用”，操作人留空 |
| 注销时间 | `deleted_at` | 来源没有软删除概念时为 NULL |
| 已废弃展示字段 | 无 | 不迁 |

来源 `role=admin` 只映射成 `user_roles.role=moderator`，不能自动获得词表维护权限；
来源 `role=super_admin` 映射成 `super_admin`。每条生效绑定同时追加一条
`user_role_records(action=grant)`，来源无法证明授予人时 actor 留空。来源停用账号除写入
当前封禁字段外，还要追加一条 `user_ban_records(action=ban)`，保留可追溯起点。

历史停用账号的封禁形态：

```text
ban_is_permanent = true
banned_until     = NULL
ban_reason       = 迁入前已停用
banned_by        = NULL
```

不能把来源停用标记丢失，否则历史封禁会被静默解除。

### 7.2 图片资产

- 为每张迁入图片建立 ID 映射；
- 保留 object key、public URL、用途、上传者、大小、content type 和创建时间；
- 2 条历史 pending 资产可以继续按 pending 迁入，由目标过期清理流程处理；
- 帖子图片和头像 URL 必须反查目标资产 ID；
- 公开 URL 不复制进帖子或用户主体。

### 7.3 帖子

| 来源形态 | 目标 |
|---|---|
| 标签数组 | `tags` + `post_tags` |
| 口味数组 | `post_flavors(stance=has)` |
| 偏好对象 | `post_flavors(stance=prefer/avoid)` |
| 餐厅文本 | `posts.canteen_id` |
| 菜系文本 | `posts.cuisine_id` |
| 预算对象 | `budget_min` / `budget_max` |
| 图片 URL 数组 | `post_images` |
| 价格 NUMERIC | 原样写目标 NUMERIC |
| 来源 UUID | 通过 `map_posts` 转 bigint |

详细规则：

- 标签 trim + NFKC 后按 `lower(name)` upsert；超过 10 字符必须人工裁决，不能无记录截断。
- 口味、菜系和餐厅必须命中目标种子或经批准的新增字典映射。
- 同一提问帖若同一口味同时出现在 prefer 和 avoid，目标复合主键会拒绝；执行前人工裁决。
- 预算只有在 JSONB 类型为 object 且 min/max 成对存在时迁入，否则双 NULL。
- 数值转整数使用 `trunc(numeric)::int`；PostgreSQL 直接 `numeric::int` 会四舍五入。
- 来源没有窗口概念，`canteen_window_id` 留空。
- 图片使用 `WITH ORDINALITY` 保序，目标 position 从 0 开始。
- 来源只提供当前帖子时不得合成 `post_histories`；目标主表直接承载当前版本，历史表保持为空。
  只有来源能提供确凿的、已经被替换的旧版本及其顺序时，才可以按证据迁入历史。

### 7.4 评论

- 用户、帖子、作者和 parent 都经映射表翻译；
- 按 parent 链递归求楼主评论，写入 `root_id`；
- 迁移脚本按任意深度处理，不能假定只有两层；
- 预检 parent 链无环、无跨帖、无缺失；
- `reply_to_user_id` 不照搬：楼主评论取帖子作者，回复取直接父评论作者；
- 来源没有软删除记录时 `deleted_at` 为空；
- 来源只提供当前评论时不得合成 `comment_histories`；首次由目标服务编辑后才产生 `revision=1`；
- 提及数组如果存在值，过滤非 UUID 和不存在用户的元素，并记录每条过滤决定；历史快照预期为空表。

### 7.5 动作表

- 来源多态点赞按目标类型拆为 `post_likes` 与 `comment_likes`；
- 无法解析目标的点赞行不迁，并进入差异报告；
- 关注、收藏和点赞的参与用户、帖子、评论全部通过映射；
- 复合主键天然去重；执行前统计重复来源行并决定是报错还是经批准去重；
- 不复制任何代理动作 ID。

### 7.6 计数器

推荐策略：

1. 插入帖子和评论时让计数列保持默认 0；
2. 不开启 `danshi.allow_counter_write`；
3. 装载点赞、收藏和评论源行，让目标触发器自动累加；
4. 完成后运行 `danshi_recount_all()`；
5. 断言所有 `fixed_rows` 为 0。

禁止先写来源计数，再装载动作行；这会产生“来源计数 + 触发器累加”的双计数。
唯一例外是无法从动作表重建的 `view_count`：帖子先以 0 插入，再用不触碰
`updated_at` 的单列 UPDATE 保存来源值。`view_count` 的 UPDATE 不受派生计数写保护触发器
限制，因此不得为此开启 `danshi.allow_counter_write`。

### 7.7 时间戳

目标 schema 没有通用 `updated_at` 触发器。ETL 直接写来源 `created_at` 和 `updated_at`，不需要禁用触发器。

时间戳对账不能直接使用目标 bigint ID。两侧统一通过 old ID 构造稳定指纹：

```sql
md5(
  string_agg(
    old_id::text || created_at::text || updated_at::text,
    '|' ORDER BY old_id
  )
)
```

目标侧先 join 映射表获得 `old_id`。`string_agg` 必须显式 `ORDER BY`，否则聚合顺序不稳定。

## 8. 通知迁入

### 8.1 类型、目标和 content

目标数据库强制：

| 类型 | 目标 | content |
|---|---|---|
| `like_post` | 帖子 | NULL |
| `like_comment` | 评论 | NULL |
| `comment` | 帖子 | 非空预览 |
| `reply` | 评论 | 非空预览 |
| `mention` | 评论 | 非空预览 |
| `follow` | 无 | NULL |

迁入前导出每种 type 的行数、目标形态、content NULL/空白数和最大长度。出现目标枚举外值即中止。

### 8.2 缺失预览的回填

不能假定通知 related ID 总是“产生通知的评论”：

- `mention` 的评论目标可以直接作为预览来源；
- `reply` 的 related comment 是被回复评论，不是新回复；
- `comment` 的 related post 是帖子，不是评论。

正确回填：

| 类型 | 规则 |
|---|---|
| `mention` | 取 related comment 正文前 100 字符 |
| `reply` | 用发送者、parent comment 与通知时间窗口唯一定位新回复；0 条或多条都不迁该通知 |
| `comment` | 用发送者、post、楼主评论形态与通知时间窗口唯一定位新评论；0 条或多条都不迁该通知 |
| like/follow 意外带 content | 经记录后置 NULL |

禁止“取第一条”或“取最近一条”。错误预览可能满足 CHECK，却把其他评论内容关联给通知，风险高于显式失败。

回填后验证正文确由通知发送者创建，且时间在通知产生时刻附近。

## 9. 历史图片、标签与评论审核

目标图片、开放标签和评论都有审核状态。历史数据没有完整审核流水，如果只把状态设为
`pass` 而不留记录，审核可追溯从迁入第一天就不成立。

已批准策略是 grandfather：

- 迁入的历史图片设 `moderation=pass`；
- 迁入的历史标签设 `moderation=pass`；
- 迁入的历史评论设 `moderation=pass`；
- 每个对象插入一条审核记录；
- `provider=legacy_migration`；
- `verdict=pass`；
- labels 为空；
- 不设置 reviewer、reviewed_at 或 supersedes_id。

这是机器形态的来源声明，不伪装成人工复核。

接受的代价：历史内容不会在迁入时重新审核。执行审批应明确记录这一风险；如果风险口径变化，应改为全量重审或人工审核，不能静默改变。

### 9.1 第一阶段只读工具

仓库提供 `cmd/danshi-legacy-migrate` 的第一阶段安全骨架，目前只支持：

```bash
SOURCE_DATABASE_URL=... TARGET_DATABASE_URL=... \
  go run ./cmd/danshi-legacy-migrate inspect

SOURCE_DATABASE_URL=... TARGET_DATABASE_URL=... \
  go run ./cmd/danshi-legacy-migrate plan
```

- 连接只能从上述两个环境变量读取，没有 DSN 命令行参数；
- 来源事务固定为 `REPEATABLE READ READ ONLY DEFERRABLE`；
- PostgreSQL 只在 `SERIALIZABLE READ ONLY` 下让 `DEFERRABLE` 产生冲突安全快照效果；
  这里保留该 GUC 是为了严格记录执行事务形态，一致性保证来自 `REPEATABLE READ`，
  不能把它宣称为 serializable safe snapshot；
- 目标必须是 PostgreSQL 18、goose v11，且只含固定词表种子；
- `plan` 获取固定 transaction-level advisory lock，但目标事务仍为只读；
- 输出只包含固定枚举与聚合计数，不包含数据库名、DSN、邮箱、正文、UUID 或 URL；
- 报告的 `inspection_level=foundation_preflight`；基础 blocker 清零不代表显式清洗 manifest、
  词表映射与逐项人工审批已经完成；
- `apply_enabled=false`、计划 `executable=false` 且 `full_source_review_complete=false`；
  当前二进制没有任何业务数据写入实现。

## 10. 两个事务局部开关

目标 schema 有两个不同开关：

| 开关 | 用途 |
|---|---|
| `danshi.allow_counter_write` | 受控写计数列 |
| `danshi.allow_hard_delete` | 受控物理删除内容实体/历史 |

另有图片资产专用 `danshi.allow_image_asset_delete`。

开关是事务局部，不是函数局部。函数如果临时改变设置，必须保存并恢复原值；ETL 自己设置后，在事务结束前后续语句仍可能处于放行状态。

推荐 ETL 不设置 counter write，也不删除目标业务行。清洗通过来源 SELECT 条件完成。

## 11. 单事务与断言

ETL 结构：

```text
BEGIN
  → 获取专用 advisory lock
  → 建立映射表
  → 按拓扑装载
  → 推进 sequences
  → 执行全部 verify 查询
  → 生成差异与摘要
COMMIT
```

任一 verify 失败通过异常让事务整体回滚。verify 不能只打印结果。

至少断言：

1. 每张映射表行数等于对应迁入表行数；
2. 所有必填外键可解析；
3. 关联表不存在跨属主、跨帖子或错餐厅关系；
4. `post_images` 总数等于来源有效图片元素数，顺序一致；
5. `comment_mentions` 总数与来源有效提及元素数一致；
6. 没有来源旧版本证据的帖子和评论，其历史表行数为 0；不得把当前版本复制为 `revision=1`；
7. grandfather 对象恰有一条 `legacy_migration` 记录；
8. 通知 type、目标和 content 形态全部合法；
9. 计数器与动作表完全一致；
10. sequence 下一个值大于现有最大 ID；
11. 时间戳指纹一致；
12. 清洗 manifest 中每个排除对象都出现在差异报告，且没有额外静默排除。

## 12. 执行前检查清单

- [ ] 重新执行全量来源勘察并冻结报告。
- [ ] 导出所有来源表行数、最大 ID 时间、最近写入时间。
- [ ] 重新评审显式清洗 manifest，确认未误伤新增真实内容。
- [ ] 导出餐厅、菜系、口味、偏好的全部 distinct 值与出现次数。
- [ ] 逐个批准词表映射；任何未命中值都已经决策为新增、归一或不迁。
- [ ] 检查同一口味同时 prefer/avoid 的帖子。
- [ ] 导出 gender 全部 distinct 值。
- [ ] 导出通知 type、目标、content 分布和最大长度。
- [ ] 计算 comment/reply 缺失预览的唯一回填率。
- [ ] 检查评论 parent 链深度、环、跨帖和缺失父节点。
- [ ] 检查所有 tag 长度和首尾空白。
- [ ] 检查全部字典文本的首尾空白与 Unicode 归一化结果冲突。
- [ ] 验证每个迁入头像和帖子图片都命中图片资产。
- [ ] 确认历史图片/标签/评论 grandfather 决策仍有效。
- [ ] 确认旧停用用户迁入后仍被封禁。
- [ ] 确认未给当前帖子/评论合成 `revision=1`；若迁入真实旧版本，逐行核对来源证据与顺序。
- [ ] 确认脚本不复制来源点赞/收藏/评论/回复计数，也不设置 `allow_counter_write`；
      `view_count` 仅按 §7.6 的单列 UPDATE 例外保存。
- [ ] 确认每张 identity 表都推进 sequence，包括空表分支。
- [ ] 在来源恢复副本上连续三次完整演练并零差异。
- [ ] 演练停写流程、同权限写探针和连接池排空。
- [ ] 备份已完成，并从备份恢复到隔离实例验证可用。
- [ ] 准备行数、映射、关联、抽样、时间戳、计数和只读 API 七类对账。
- [ ] 明确最后无损回滚点与目标产生新写入后的处置授权。
- [ ] 来源数据库保留至少四周只读。

## 13. 迁入后验证

### 13.1 数据对账

- 清洗前、排除、迁入三列行数守恒；
- 所有映射表行数守恒；
- 200 条分层随机抽样逐字段对比；
- 图片数组顺序对比；
- 评论楼层与回复目标对比；
- 时间戳指纹对比；
- 计数器与动作表对比；
- 审核 grandfather 记录对比；
- 词表引用和停用条目展示对比。

### 13.2 只读 API 验证

使用目标服务验证：

- 真实用户密码仍可登录；
- 历史令牌不能继续使用，用户需要重新登录；
- 信息流、详情、资料、评论、通知和收藏读取；
- 分享帖价格格式；
- 提问帖预算和偏好；
- 餐厅、菜系、口味和标签筛选；
- 图片和头像 URL；
- 已封禁账号不能登录；
- `/ready` 和 schema 版本正确。

### 13.3 写入 smoke

在最后无损回滚点之后才执行：

- 新用户注册与验证码；
- 登录和会话撤销；
- 发帖、图片上传与审核；
- 评论、回复、点赞、收藏和通知；
- 编辑产生连续 revision 并重新审核；
- 软删除与恢复；
- 词条提议和管理员审批。

所有测试数据应带明确迁移 smoke 标记，并在受控测试账号和环境中执行；生产正式数据域不应长期保留冒烟内容。

## 14. 迁入后防线

为避免再次需要同类清洗：

1. 生产和测试使用独立数据库。
2. 生产禁止写入冒烟测试帖子、评论和通知。
3. 注册应用邮箱域名白名单和验证码验证。
4. 数据库外键替代裸多态 ID。
5. 计数器由触发器维护并定期只读对账。
6. 图片、评论层级、通知目标和词表关系受 schema 约束。
7. 审核流水与被替换的内容历史分别追加不可篡改；审核只按对象和字段归属当前内容。
8. 备份必须有异机副本，并定期做隔离恢复演练；仓库当前尚未提供备份自动化实现，部署方不能仅凭“生成了备份文件”宣称可恢复。

## 15. 存量邮箱运营事项

历史清洗估算中，迁入用户有一部分邮箱不满足默认注册域名。域名规则只约束新注册，登录不能因此阻断存量账号。

受控 runbook 应输出存量不合规账号清单，由运营联系用户完成邮箱更新。公开文档只保留数量和规则，不保存真实邮箱、昵称或用户 ID。

历史估算为：

- 13 个账号满足默认域名；
- 18 个账号需要运营处理；
- 其中 1 个关键运维账号可按批准映射直接修正；
- 其余账号按角色、内容产出和活跃度分级联系。

执行时必须重算，不能继续使用历史数量作为完成证明。
