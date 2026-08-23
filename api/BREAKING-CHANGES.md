# API 变更登记表

本登记表逐条对应 `go-rewrite-plan.md` §4 的实际标注。按小节标题中的
“**破坏性**”重新计数，原重写计划共 **12 项**；后续产品批次确认的破坏性变更继续编号。
新增端点、附加字段和非破坏行为变更另列在文末。

## 破坏性变更（17 项）

### BC-001 / §4.3：分页参数改为严格校验

**动机**：消除非法参数被静默回退或钳制的隐式行为，使客户端错误可以被稳定发现，
并让 `page`、`limit` 的边界成为明确契约。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| `page=abc` | 回退为 `1`，返回 200 | 返回 422，`field=page`、`code=invalid_format` |
| `limit=999` | 钳制为 `100`，返回 200 | 返回 422，`field=limit`、`code=out_of_range` |
| `limit=-5` | 钳制为 `1`，返回 200 | 返回 422，`field=limit`、`code=out_of_range` |
| 缺省值 | `page=1`、`limit=20` | 不变 |

新边界为 `page >= 1`、`1 <= limit <= 100`。

**前端影响清单**：

- [ ] 所有列表请求在发出前保证 `page`、`limit` 是整数且位于上述范围内。
- [ ] 422 走统一字段错误处理，不再依赖服务端自动修正。
- [ ] 保留对既有钳制逻辑的回归测试；当前帖子列表仓库已将 `page` 下限钳为 1、
      `limit` 钳在 1–50，预计无需功能改动。

### BC-002 / §4.4：`price` 从 JSON number 改为十进制字符串

**动机**：数据库本来使用 `NUMERIC(10,2)`；字符串契约避免二进制浮点丢失十进制精度，
并固定显示到两位小数。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| 请求类型 | number / float | 十进制 string |
| 响应类型 | number / float | 固定两位小数的 string，如 `"18.50"` |
| 空值 | `null` | `null`，不会变成 `"0.00"` |
| 非法值 | 依赖 float/Pydantic 行为 | 422 `field=price`、`code=invalid_format` |

v2 拒绝负数、`NaN`、`Inf`、科学计数法、超过 8 位整数或 2 位小数的输入。

**前端影响清单**：

- [ ] `Post.price` 从 `number` 改为 `string | null`。
- [ ] 修改 `post_converters.ts` 中的 number 类型判断。
- [ ] 修改 `post_card.tsx` 的 number 判断及 `toFixed` 调用。
- [ ] 修改 `post_detail_screen.tsx` 的 number 判断与渲染。
- [ ] 发帖请求按十进制字符串提交；计算时显式使用十进制库或受控解析，不直接依赖浮点。

### BC-003 / §4.5：401 与 403 按身份语义重新划分

**动机**：401 应表示凭据缺失或失效，403 应表示身份有效但权限不足；同时统一脱敏文案，
避免通过认证错误细节推断令牌或用户状态。

| 情形 | Python v1 | Go v2 |
|---|---|---|
| header 缺失/格式非法、签名失败、过期、token 类型错误 | 401，可能返回多种具体文案 | 401，统一为“未登录或登录已失效” |
| token 有效但用户行不存在，访问管理端 | 403 特判 | 401，凭据视为失效 |
| 有效普通用户访问 admin/super-admin 能力 | 403 | 403 |
| 有效但已停用的用户 | 认证行为不统一 | 403 |

**前端影响清单**：

- [ ] 401 统一触发会话失效/重新登录流程。
- [ ] 403 只展示权限或账号状态提示，不清除仍有效的登录态。
- [ ] 删除基于中文错误文案或旧 403 特判的分支。
- [ ] Bearer header 按标准格式发送，避免非标准 scheme 或空 token。

### BC-004 / §4.6：点赞与收藏响应统一为 snake_case

**动机**：移除全站仅存的四个 camelCase 例外，保持 JSON 字段命名规则唯一。

| Python v1 字段 | Go v2 字段 |
|---|---|
| `isLiked` | `is_liked` |
| `likeCount` | `like_count` |
| `isFavorited` | `is_favorited` |
| `favoriteCount` | `favorite_count` |

**前端影响清单**：

- [ ] 所有写入与读取统一使用 snake_case 字段。
- [ ] 当前 `posts_repository.ts` 已兼容两种形态，确认 v2 稳定后删除 camelCase fallback。
- [ ] Mock、fixture 与类型声明同步改为 snake_case。

### BC-005 / §4.9：注册接口不再接受头像

**动机**：取消注册阶段对外部头像 URL 的 HEAD 探测及其 SSRF 防护复杂度；头像改为登录后
通过标准上传流程设置，从而统一资产所有权、用途和 ready 状态校验。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| `POST /auth/register` 请求 | 可带 `avatar_url` | 不接受 `avatar_url` |
| 新用户头像 | 可在注册时引用白名单 URL | 默认为空 |
| 设置头像 | 注册或资料更新 | 注册后上传本人 avatar 资产，再更新资料 |

**前端影响清单**：

- [ ] 注册请求和注册类型删除 `avatar_url`。
- [ ] 检查并移除注册页头像选择/预取 UI。
- [ ] 若需要设置初始头像，注册成功并登录后走 presign → 上传 → complete → 更新资料流程。
- [ ] 同步清理 mock 中只服务于旧注册头像的分支。

### BC-006 / §4.11：新增注册邮箱域名白名单

**动机**：保证新账号属于允许的校园/项目邮箱域；验证码只能证明邮箱可收信，不能代替域名准入。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| 注册域名 | 任意合法邮箱 | 默认仅 `fdueat.com`、`m.fudan.edu.cn`、`fudan.edu.cn` |
| 非白名单注册 | 可继续验证码流程 | 422 `field=email`、`code=invalid_domain` |
| 存量用户登录 | 不受限制 | 仍不做域名校验，保持可登录 |

**前端影响清单**：

- [ ] 注册页展示允许的邮箱域名，并在提交前给出本地提示。
- [ ] 处理 `invalid_domain` 字段错误。
- [ ] 登录页不得复制注册域名限制，以免锁死存量非白名单账号。

### BC-007 / §4.14：删除 `users.hometown`

**动机**：该字段只被传输和展示，没有业务逻辑消费者；删除无效契约面与数据库列。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| 注册请求 | 可含 `hometown` | 字段删除 |
| 资料更新请求 | 可含 `hometown` | 字段删除 |
| 用户响应 | 含 `hometown` | 字段删除 |

`gender` 不在本次删除范围内，仍可通过既有资料更新入口修改。

**前端影响清单**：

- [ ] 用户、注册和资料更新类型删除 `hometown`。
- [ ] 删除 hometown 表单、展示和 mock/fixture 字段。
- [ ] 不要误删 `gender` 相关 UI 与类型。

### BC-008 / §4.15：所有资源主键从 UUID string 改为 integer

**动机**：新 schema 使用 `bigint identity`，降低主键、外键和复合索引宽度，并贴合 GORM 的
整数 ID 约定。可枚举 ID 的授权风险由所有按 ID 详情接口强制鉴权处理。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| JSON ID | 36 字符 UUID string | number，如 `1001` |
| 路径参数 | UUID 文本 | 正整数文本 |
| 客户端模型 | `id: string` | `id: number` |

**前端影响清单**：

- [ ] 全量扫描用户、帖子、评论、通知、上传、会话、字典、审核等 ID 类型并改为 number。
- [ ] 删除 UUID 正则、长度 36 判断和依赖字符串语义的比较。
- [ ] React Navigation 等字符串 route param 在调用 API 前显式解析为正整数。
- [ ] 检查 Map/Set key、缓存 key、序列化与 mock fixture，避免 string/number 混用。
- [ ] 不依赖 ID 难猜性；按 ID 请求继续携带认证信息并正确处理 401/403/404。

### BC-009 / §4.17：API 路由前缀从 `/api/v1` 改为 `/api/v2`

**动机**：新旧 schema 和主键类型不兼容，不能在同一数据面原地替换；版本前缀允许网关把
Python v1/旧库与 Go v2/新库并行隔离。

| 项目 | Python 服务 | Go 服务 |
|---|---|---|
| 路由前缀 | `/api/v1` | `/api/v2` |
| 数据库 | 旧 schema，切换前置只读 | 新 schema |
| 切换方式 | 网关按路径分流 | 网关按路径分流，不做百分比灰度 |

**前端影响清单**：

- [ ] v2 客户端 API base path 全量切换到 `/api/v2`。
- [ ] 不把部分 v1 路径与部分 v2 路径混入同一客户端版本。
- [ ] 老版本 App 继续使用 v1；准备好旧库只读时写操作失败的降级展示。
- [ ] v2 发布、网关分流和旧服务下线按迁移计划执行，不能只改客户端常量。

### BC-010 / §4.19：口味、菜系、餐厅改为后端受控词表

**动机**：把选择项真源从自由文本或前端常量迁回数据库，获得引用完整性、停用能力和统一配置；
新增词条通过提议/审批流进入。

| 项目 | Python v1 | Go v2 |
|---|---|---|
| 口味 | 前端可提交任意自由文本 | 只能选择后端启用词条 |
| 菜系/餐厅 | 前端硬编码固定项 | 从 `/config` 获取启用词条 |
| 新词条 | 无统一后端流程 | 通过 dictionary suggestion 提议并由管理员审批 |

**前端影响清单**：

- [ ] 口味自由输入改为选择器，并提供“提交建议”入口。
- [ ] 餐厅、菜系、口味从 `/config` 加载，不再以本地 fallback 为真源。
- [ ] 餐厅选择后展示窗口二级选择。
- [ ] 处理停用词条、提议 pending/approved/rejected 状态和审核理由。
- [ ] 数据迁移前对存量自由口味逐项映射，客户端不得假设所有旧值都能直接提交到 v2。

### BC-011 / §4.20：帖子地点字段契约重构

**动机**：用稳定的餐厅 code 隔离显示名称变更，并加入餐厅窗口这一层级；响应返回结构化对象，
避免客户端猜测 code、名称与校区之间的关系。

| 场景 | Python v1 | Go v2 |
|---|---|---|
| 创建/更新请求 | `canteen: string` 中文名 | `canteen_code: string` |
| 窗口请求字段 | 无 | `canteen_window_id: number | null` |
| 帖子响应 | `canteen: string` | `canteen: {code,name,campus} | null` |
| 窗口响应字段 | 无 | `canteen_window: {id,name,floor} | null` |
| 搜索筛选 | `canteen=<中文名>` | `canteen_code=<稳定 code>` |

**前端影响清单**：

- [ ] 发帖草稿与请求模型把 `canteen` 改为 `canteen_code`。
- [ ] 新增并校验可空的 `canteen_window_id`；窗口必须属于所选餐厅。
- [ ] 卡片、详情与编辑页按结构化 `canteen` / `canteen_window` 渲染。
- [ ] 搜索筛选参数改为 `canteen_code`。
- [ ] 旧草稿中的中文餐厅名在迁移或首次打开时映射为稳定 code。

### BC-012 / RBAC：单角色改为多角色能力并集

**动机**：把词表维护、内容审核和超级管理职责拆开，并允许同一用户同时承担多个职责；
授权端点只检查业务能力，不再把 `admin` 当作含义不断扩张的万能角色。

| 项目 | 旧契约 | 新契约 |
|---|---|---|
| 用户角色响应 | `role: "user" | "admin" | "super_admin"` | `roles: ("dict_reviewer" | "moderator" | "super_admin")[]` |
| 普通用户 | `role="user"` | `roles=[]`，由无绑定表达 |
| 管理员角色 | `admin` 同时拥有词表和内容权限 | `dict_reviewer` 与 `moderator` 分离 |
| 角色调整请求 | `{ "role": "..." }` 覆盖单值 | `{ "role": "...", "action": "grant" | "revoke" }` 变更一项绑定 |
| 权限组合 | 只能三选一 | 所有绑定角色的能力并集 |

存量 `admin` 只迁为 `moderator`，不会自动获得 `dict_reviewer`；存量 `super_admin`
迁为同名绑定。`admin` 取值从请求、响应和 OpenAPI 枚举中删除。

**前端影响清单**：

- [ ] 用户、登录态和管理端用户模型把单值 `role` 改为 `roles` 数组。
- [ ] 词表管理入口检查 `dict_reviewer` 或 `super_admin`，内容审核入口检查
      `moderator` 或 `super_admin`；不要把角色判断散落到页面组件。
- [ ] 角色管理 UI 对每个角色分别发送 `grant` / `revoke`，并以响应的 `roles` 为准刷新状态。
- [ ] 删除所有 `admin` 角色分支；需要兼任词表与内容管理时同时绑定两个角色。

### BC-013 / 信息流与通知：offset 改为复合游标分页

**动机**：信息流与通知是持续变化的瀑布流。offset 在两次请求之间发生插入时会重复上一页
边界项，发生删除时会跳过未读项；`(created_at, id)` 复合游标为现有倒序建立稳定全序。

| 场景 | 旧契约 | 新契约 |
|---|---|---|
| `GET /api/v2/posts?sort_by=latest` 请求 | `page`、`limit` | `cursor`、`limit`；不传 cursor 从最新开始 |
| `GET /api/v2/notifications` 请求 | `page`、`limit` | `cursor`、`limit` |
| 上述响应 `pagination` | `{page,limit,total,total_pages}` | `{limit,next_cursor,has_more}` |
| `GET /api/v2/posts` 的 `hot/trending/price` | offset | 仍为 offset，本批不变 |
| 评论、搜索、用户页、管理端、词表 | offset | 仍为 offset，本批不变 |

游标是加密认证的不透明字符串；无效、跨端点使用或被篡改时返回 422，字段为 `cursor`、
代码为 `invalid_format`。`latest` 下显式请求 `page > 1` 返回 422，避免把旧客户端请求静默
解释成第一页；`page=1` 等价于不传 page，便于只显式指定首页的客户端迁移。

**前端影响清单**：

- [ ] 信息流 latest 与通知状态删除页码和总页数依赖，保存 `next_cursor` 并按 `has_more` 续取。
- [ ] 刷新时清空旧 cursor；加载更多时原样回传，不解析、不拼接、不跨筛选条件复用。
- [ ] `next_cursor=null` 或 `has_more=false` 时停止加载。
- [ ] 帖子非 latest 排序继续使用 `page`；评论继续使用 offset，不切换到 cursor。

### BC-014 / 公共配置：移除恒真的 `is_active`

**动机**：`GET /api/v2/config` 只查询启用餐厅和启用窗口，原响应中的
`canteens[].is_active` 与 `canteens[].windows[].is_active` 因而恒为 `true`，不能向客户端
传递任何额外状态。

| 项目 | 旧契约 | 新契约 |
|---|---|---|
| `canteens[]` | 含 `is_active: true` | 移除 `is_active` |
| `canteens[].windows[]` | 含 `is_active: true` | 移除 `is_active` |
| 停用项可见性 | 不返回 | 不变，仍不返回 |

**前端影响清单**：

- [ ] 删除公共配置模型中餐厅和窗口的 `is_active` 必填字段。
- [ ] 不再依赖恒真字段筛选；收到的餐厅和窗口都可直接视为当前可选项。
- [ ] 管理端词表响应中的真实 `is_active` 不受本项影响。

### BC-015 / 窗口提议：父餐厅由数字主键改为稳定编码

**动机**：公共配置把餐厅 `code` 作为 `canteens[].id` 暴露，客户端拿不到数据库数字主键；
窗口提议继续要求数字主键会形成无法构造的请求。

| 项目 | 旧契约 | 新契约 |
|---|---|---|
| 端点 | `POST /api/v2/dictionary-suggestions` | 不变 |
| 窗口提议父餐厅字段 | `parent_canteen_id: integer` | `parent_canteen_code: string` |
| 同批父餐厅提议 | `parent_suggestion_id` | 不变 |
| 无效或已停用编码 | 无稳定的客户端可用输入 | 404 `dict_item_not_found` |

响应和数据库审核记录中的 `parent_canteen_id` 仍是已解析的内部关联标识；本项只改变用户提交
窗口提议时的请求体字段。

**前端影响清单**：

- [ ] 窗口提议从公共配置餐厅对象的 `id` 读取稳定 code，并发送为 `parent_canteen_code`。
- [ ] 删除发送 `parent_canteen_id` 的旧逻辑；联合提议继续发送 `parent_suggestion_id`。
- [ ] 对 404 `dict_item_not_found` 提示刷新公共配置后重新选择餐厅。

### BC-016 / 编辑历史只返回旧版本，审核队列移除版本锚点

**动机**：主表是当前内容的唯一真源；历史表只需要保存被编辑替换掉的旧版本。审核员处理的
也是对象当前内容，无需把审核流水锚定到一行 history。

| 项目 | 旧契约 | 新契约 |
|---|---|---|
| 创建帖子/评论后的 history | 含与主表相同的 `revision=1` | 空数组，不写历史 |
| 首次编辑后的 `revision=1` | 编辑后的当前版本 | 编辑前被替换的创建版本 |
| 后续 history | 最新一行与主表相等 | 只含旧版本，主表单独保存当前版本 |
| 主表与最新 history 不一致 | 编辑返回 409 | 不再比较，不因此返回 409 |
| `GET /api/v2/admin/moderation-records/pending` 单项 | 含 `post_history_id`、`comment_history_id` | 删除这两个字段 |
| 编辑前未处理的待复核记录 | 仍可能展示 | 保留流水，但队列只展示同对象、同场景、同字段的最新机审 |

帖子与评论 history 的 `revision` 仍从 1 连续递增、不可重复或跳号，只是从首次编辑开始计数。
人工复核继续通过 `supersedes_id` 追加流水，并强制同一对象、同一字段；版本不再参与约束。

**前端影响清单**：

- [ ] 历史页接受“内容从未编辑时列表为空”，不要把空列表视为数据损坏。
- [ ] 历史列表把每一行解释为被替换的旧版本；当前版本始终来自帖子或评论主体接口。
- [ ] 管理端审核类型删除 `post_history_id`、`comment_history_id`，不要按它们缓存或定位内容。
- [ ] 复核页以队列返回的 `content` 展示当前内容；编辑后旧待办会消失，刷新后处理最新记录。
- [ ] 不再针对“主表与最新历史不一致”的旧 409 提示用户刷新；其它 409 仍按各自 `error_code` 处理。

### BC-017 / 图片上传回传真实机审结论，帖子改为联合复核

**动机**：帖子正文与附图共同决定整帖能否发布。旧契约把每条机器审核记录当作独立待办，
导致同一帖的正文和多张图片分散展示、分开裁决；上传完成响应还会丢弃同步图片机审已经得到的
结论，并把 `status` 固定写成 `pending`。

| 端点或项目 | 旧契约 | 新契约 |
|---|---|---|
| `POST /api/v2/uploads/{upload_id}/complete` | 无 `moderation`；`status` 固定为 `pending`；总是返回 `public_url` | 新增真实 `moderation`；`status` 来自资产；同步 `block` 省略 `public_url` |
| `GET /api/v2/admin/moderation-records/pending` 的帖子待办 | 正文和每张图片各占一个记录条目 | 同一帖子只占一个条目，`post.text` 与 `post.images[]` 同步给出当前内容、机审结论、标签、分数和图片签名 URL |
| 帖子队列准入 | 任一对象的 `review` 都可能独立出现 | 全部对象已有最终结论、无 `block` 且至少一个 `review` 时才出现；任意 `block` 直接驳回 |
| `PUT /api/v2/admin/posts/{post_id}/review` | 只 supersede 一条正文机审记录，响应为 `moderation_record_id` | 一次裁决处理整帖全部当前 `review` 记录，响应改为 `moderation_record_ids[]` |
| `PUT /api/v2/admin/moderation-records/{moderation_record_id}/review` | 可单独处理帖子正文或已绑定帖子图片 | 帖子对象必须改走整帖复核端点；评论、标签、用户和非帖子图片仍逐条处理 |

帖子状态的唯一聚合优先级是：任意 `block` → `rejected`；否则任意 `review` → `pending`；
否则全部 `pass` → `approved`。尚未返回最终图片结论的 `pending` 只表示继续等待机器审核，
不会提前进入人工队列。编辑帖子后使用同一规则重新计算。

整帖人工裁决不放宽 `uq_mr_supersedes`：服务为本次处理的每一条机器 `review` 分别追加一条
`provider=manual` 记录，所有记录共享同一审核人、时间、结论和反馈。

**前端影响清单**：

- [ ] 上传完成后读取 `moderation`：`pending` 时轮询 `GET /api/v2/uploads/{upload_id}`，
      `block` 时提示用户并禁止把该图片加入发帖 `images`。
- [ ] 不再假设 complete 一定含 `public_url`；同步 `block` 时该字段被省略。
- [ ] 管理端按 `target_type=post` 读取 `post.text` 与完整 `post.images[]`，不要再把同帖图片记录
      拼成多个待办。
- [ ] 帖子人工复核只调用 `/admin/posts/{post_id}/review`，并把响应审计字段改为
      `moderation_record_ids` 数组。
- [ ] 作者详情可以读取新增的 `moderation.status` 与 `moderation.issues[]`；该对象不会返回给
      非作者，客户端不得据此推断其他用户内容的审核原因。

## 新增、勘误与行为变更（不计入上述破坏性变更）

### 社交关系：关注统计与列表统一排除已注销用户

- **类别**：一致性勘误，非破坏。
- **影响端点**：`GET /api/v2/users/{user_id}`、`GET /api/v2/users/{user_id}/following`、
  `GET /api/v2/users/{user_id}/followers`。
- **新口径**：`follower_count`、`following_count`、列表 `pagination.total` 与实际返回条数均只
  统计未注销用户；软删除不会物理删除历史关注动作行。
- **分页边界**：关注和粉丝列表继续使用 offset 分页，本项不改变分页协议。

### 账号资料：注册补齐 `gender=other`

- **类别**：既有枚举值变得可达，附加式行为，非破坏。
- **影响端点**：`POST /api/v2/auth/register` 开始与资料更新、数据库约束一致地接受
  `male`、`female`、`other` 或空值；其他字符串仍返回 422 `invalid_enum`。
- **前端影响**：注册页可以与资料编辑页共用同一性别枚举。

### 误杀恢复：审核流水增加可查询的操作人列

- **类别**：审计字段补全，附加式行为，非破坏。
- **影响端点**：`PUT /api/v2/admin/posts/{post_id}/restore`、
  `PUT /api/v2/admin/comments/{comment_id}/restore`。
- **新语义**：新建的 `provider=admin_restore` 流水在 `reviewer_id` 与 `reviewed_at` 列记录实际
  操作人和时间，`raw_response` 继续保留 `action=restore`；恢复仍不是人工复核，不携带
  `supersedes_id`。
- **兼容性**：迁移前无法可靠回填操作人的存量恢复流水允许两列同时为空；机器审核流水仍
  禁止携带这两列。

### 图片响应：新增三档图片与头像缩略图字段

- **类别**：附加字段，非破坏。
- **动机**：让信息流和详情按展示场景加载实时派生缩略图，同时保留原图 URL 作为资产身份。
- **影响端点**：
  - `GET /api/v2/posts`
  - `GET /api/v2/posts/{post_id}`
  - `GET /api/v2/users/{user_id}/posts`
  - `GET /api/v2/users/{user_id}/favorites`
  - `GET /api/v2/search/posts`
- **新增字段**：帖子条目增加与 `images` 同序等长的 `image_displays`、`image_thumbs`；
  帖子作者增加 `avatar_thumb_url`。原 `images` 与 `avatar_url` 语义不变。
- **私有图片边界**：上传者和管理端短期签名读取响应不生成派生档位，避免追加查询参数使
  COS 签名失效；墓碑 URL 原样保留，不追加处理参数。
- **前端影响**：列表使用 `image_thumbs`，详情使用 `image_displays`，查看大图或保存继续使用
  `images`；无图时三个数组均为 `[]`。

### §3.4：新增本人账号注销端点

- **类别**：新增端点，非破坏。
- **动机**：补齐既有用户软删除语义的生产入口，并在同一事务撤销全部登录会话。
- **新增端点**：`DELETE /api/v2/users/{user_id}`，只允许当前登录用户注销本人账号。
- **注销结果**：账号不能再次登录或释放原邮箱，且从搜索和正常用户列表消失；既有内容保留并将作者展示为“已注销用户”。
- **前端影响**：设置页可增加明确的账号注销确认交互；服务端当前不要求密码或验证码二次确认。

### RBAC：新增单用户取证端点

- **类别**：新增端点，非破坏。
- **动机**：让内容审核员按明确目标查看单个用户资料与完整帖子历史，不授予全量用户列表能力。
- **新增端点**：
  - `GET /api/v2/admin/users/{user_id}`
  - `GET /api/v2/admin/users/{user_id}/posts`
- **前端影响**：用户列表仍只对超级管理员开放；内容审核页应从已知用户跳转到上述取证端点，
  并按返回的帖子状态及删除标记展示完整历史。

### 图片审核：新增私有图片签名读取端点

- **类别**：新增端点与既有管理响应 URL 替换，非破坏。
- **动机**：`review/block` 图片对象转为私有后，仍要允许上传者辨认自己的图片，并允许审核员完成待复核与误杀恢复。
- **新增端点**：
  - `GET /api/v2/uploads/{upload_id}`：只允许上传者本人，返回短期 `image_url`；
  - `GET /api/v2/admin/images/{image_asset_id}`：复用既有内容审核能力，返回单图详情与短期 `image_url`。
- **既有响应**：管理端待复核队列的图片 `content`、管理端单用户详情的 `avatar_url`
  改为短期签名 URL；公开列表与详情仍返回原 `PublicURL`，筛选行为不变。
- **前端影响**：管理页面不得持久化签名 URL；图片加载因 1 小时 TTL 过期时重新请求相应 API。

### §4.1.1：错误响应新增顶层 `error_code`

- **类别**：附加字段，非破坏。
- **动机**：让客户端按稳定机器码区分同一 HTTP 状态下的业务原因，而不是匹配中文文案。
- **前后对比**：v1 错误体只有 `code/message/data`；v2 非 2xx 必有非空
  `error_code`，成功响应不包含该字段。
- **前端影响**：先按 HTTP 状态分流，再按 `error_code` 细化；未知码使用通用兜底。

### §4.12：保留通知 `content` 并补全 `mention` 类型

- **类别**：勘误与约束补全，非破坏。
- **动机**：复核确认 comment/reply/mention 会写入正文预览且前端会渲染，删除 `content` 会回归；
  `mention` 也是已存在的真实类型。
- **前后对比**：不执行曾误提的 `content` 删除；v2 将其限制到 100 字符，并把 `mention`
  纳入正式枚举及目标类型约束。
- **前端影响**：继续渲染 `content`，保留 `mention` 分支；无需迁移到替代字段。

### §4.13：新增公共 `GET /api/v2/config`

- **类别**：新增端点，非破坏。
- **动机**：让后端成为帖子类型、餐厅/窗口、菜系和口味的唯一配置真源。
- **前后对比**：v1 无该端点，前端四个 fetcher 返回本地常量；v2 返回
  `post_types/canteens/cuisines/flavors`，窗口内嵌于餐厅。
- **前端影响**：改为真实请求，可做一次性缓存；删除把 fallback 当真源的路径。

### §4.16：新增词条提议与词表管理端点

- **类别**：新增端点，非破坏。
- **动机**：受控词表仍需可审计的扩充、停用与有限物理删除流程。
- **新增用户端点**：
  - `POST /api/v2/dictionary-suggestions`
  - `GET /api/v2/dictionary-suggestions/mine`
- **新增管理员端点**：
  - `GET /api/v2/admin/dictionary-suggestions`
  - `POST /api/v2/admin/dictionary-suggestions/{suggestion_id}/approve`
  - `POST /api/v2/admin/dictionary-suggestions/{suggestion_id}/reject`
  - `POST /api/v2/admin/flavors`
  - `PATCH /api/v2/admin/flavors/{flavor_id}`
  - `DELETE /api/v2/admin/flavors/{flavor_id}`
  - `POST /api/v2/admin/cuisines`
  - `PATCH /api/v2/admin/cuisines/{cuisine_id}`
  - `DELETE /api/v2/admin/cuisines/{cuisine_id}`
  - `POST /api/v2/admin/canteens`
  - `PATCH /api/v2/admin/canteens/{canteen_id}`
  - `DELETE /api/v2/admin/canteens/{canteen_id}`
  - `POST /api/v2/admin/canteens/{canteen_id}/windows`
  - `PATCH /api/v2/admin/canteen-windows/{window_id}`
  - `DELETE /api/v2/admin/canteen-windows/{window_id}`
- **前端影响**：新增提议提交/状态 UI 和管理端审批/维护 UI；默认使用 `PATCH is_active=false`
  停用。只有从未被引用且无审核历史的条目可物理删除，否则处理 409。

### §4.18：`POST /auth/logout` 开始真正撤销会话

- **类别**：行为变更，路径与请求/响应形状不变。
- **动机**：登出必须立即使当前 token 失效。
- **前后对比**：v1 logout 是空壳，旧 token 仍可使用；v2 撤销会话，后续请求返回 401。
- **前端影响**：登出前取消尾随请求和预取，立即清除本地凭据；不要在登出动画期间继续使用旧 token。

### §4.21：已注册邮箱同样消耗验证码限流配额

- **类别**：行为变更，请求/成功响应形状不变。
- **动机**：修复通过第二次请求的 200/429 差异判断邮箱是否已注册的枚举旁路。
- **前后对比**：v1 已注册邮箱提前返回且不计限流；v2 与未注册邮箱走相同的挑战行、冷却、
  窗口计数与 200/429 时机，但不向已注册邮箱投递邮件，并写入随机不可用 digest。
- **前端影响**：继续按既有逻辑处理 429 与 `Retry-After`，不需要新增响应分支。

### §3.x：新增登录会话管理端点

- **类别**：新增端点，非破坏。
- **动机**：Python v1 只有单设备 `logout`，无法查看或撤销其它设备的登录态。
- **新增端点**：
  - `GET /api/v2/auth/sessions`：只返回本人未撤销且未过期的会话，并标记当前设备；
  - `DELETE /api/v2/auth/sessions/{id}`：踢掉本人指定会话，不能操作他人会话；
  - `POST /api/v2/auth/logout-all`：撤销本人全部未撤销会话。
- **撤销语义**：一律只写 `revoked_at`，不物理删除审计记录；撤销后旧 access/refresh 立即返回脱敏 401。
- **前端影响**：设置页可新增设备管理列表；退出登录仍走既有 `logout`。

### §5.2.8：新增编辑历史端点

- **类别**：新增端点，非破坏。
- **动机**：v2 新增帖子与评论的编辑能力，PRD 要求被替换的旧版本留痕并可查看编辑历史；
  Python v1 没有编辑能力，因此基线清单里不存在对应端点。
- **新增端点**：
  - `GET /api/v2/posts/{post_id}/history`
  - `GET /api/v2/comments/{comment_id}/history`
- **可见性**：仅未删除内容的作者本人可读，按 revision 倒序返回不可变旧版本；内容未编辑时为空。
- **前端影响**：编辑入口旁可提供“查看历史版本”；非作者不应展示该入口。

### §5.2.8：新增评论编辑端点

- **类别**：新增端点，非破坏。
- **动机**：与帖子编辑对齐，PRD 已登记“评论可编辑、旧版本留痕、被编辑标识”。
- **新增端点**：`PUT /api/v2/comments/{comment_id}`，仅作者本人可编辑。
- **并发语义**：编辑先对主体行加锁，把编辑前正文严格追加为 `max(history.revision)+1`，
  再更新主体；首次编辑产生 revision 1。
- **前端影响**：评论需渲染“已编辑”标识；历史为空表示尚未编辑。

### §6.9：新增人工复核队列与误杀恢复端点

- **类别**：新增端点，非破坏。
- **动机**：机器审核会产生 `review` 待定与 `block` 误杀，PRD §6.9 要求人工复核闭环，
  Python v1 没有对应入口。
- **新增端点**：
  - `GET /api/v2/admin/moderation-records/pending`：待人工复核队列；
  - `PUT /api/v2/admin/moderation-records/{moderation_record_id}/review`：写入人工结论；
  - `PUT /api/v2/admin/posts/{post_id}/restore`
  - `PUT /api/v2/admin/comments/{comment_id}/restore`
- **审计语义**：恢复操作追加不可变 `moderation_records` 行，`provider='admin_restore'`、
  `verdict='pass'`，只关联被恢复的内容对象，并在 `reviewer_id/reviewed_at` 记录实际操作人；
  不伪造 `manual` 行，以免破坏人工复核状态机与 `uq_mr_supersedes` 语义。图片
  `moderation != pass` 的内容拒绝恢复，返回 409 `image_not_approved`。
- **权限**：复用内容审核能力，不新增角色。
- **前端影响**：管理端新增复核队列页；恢复为幂等的显式动作。

### §6.9：新增图片审核供应商回调入口

- **类别**：新增端点，非破坏。
- **动机**：异步图片审核必需的供应商 ingress，不是业务管理端点。
- **新增端点**：`POST /api/v2/moderation/tencent-ci/callback`。
- **鉴权**：以 SHA-256 等长摘要做常量时间令牌比较；未配置返回 503，令牌错误返回 403，
  非法载荷返回 `moderation_callback_invalid`。
- **前端影响**：无，该端点不面向客户端。

### §5.3、§5.4、§6.9：补齐词表、话题标签与复核队列管理能力

- **类别**：新增端点与既有查询参数扩展，非破坏。
- **动机**：停用词表项此前无法从管理端列表找回；话题标签的重命名、合并、下架、恢复和热门统计没有生产入口；人工复核队列也无法按供应商审核标签收窄。
- **新增词表端点**：
  - `GET /api/v2/admin/flavors`
  - `POST /api/v2/admin/flavors/{flavor_id}/enable`
  - `GET /api/v2/admin/cuisines`
  - `POST /api/v2/admin/cuisines/{cuisine_id}/enable`
  - `GET /api/v2/admin/canteens`
  - `POST /api/v2/admin/canteens/{canteen_id}/enable`
  - `GET /api/v2/admin/canteen-windows`
  - `POST /api/v2/admin/canteen-windows/{window_id}/enable`
- **词表语义**：四个列表默认包含启用与停用项，可用 `is_active` 筛选，并以 `cursor/limit` 翻页；显式启用后条目重新进入公共 `/config`。既有 `PATCH is_active=false` 仍是停用入口，既有 `DELETE` 仍是受外键约束的物理删除；被帖子或审核历史引用时返回 `409 dict_item_in_use`。
- **新增话题标签端点**：
  - `GET /api/v2/admin/tags`
  - `PATCH /api/v2/admin/tags/{tag_id}`
  - `POST /api/v2/admin/tags/{tag_id}/merge`
  - `DELETE /api/v2/admin/tags/{tag_id}`
  - `POST /api/v2/admin/tags/{tag_id}/restore`
  - `GET /api/v2/admin/tags/hot`
- **标签语义**：列表支持 `name` 模糊匹配、`moderation`、`is_deleted` 与游标分页。重命名遇到大小写不敏感同名项返回 `409 tag_name_conflict` 并要求改走合并；合并在单事务内去重迁移 `post_tags` 后下架源标签。下架和恢复只翻转 `deleted_at`，不删除关联。热门统计实时按未下架标签在 `status=approved AND deleted_at IS NULL` 帖子中的关联数排序，不做时间衰减、加权或缓存。
- **复核队列扩展**：`GET /api/v2/admin/moderation-records/pending` 新增可选 `label`，对 `moderation_records.labels` 做单个审核标签精确筛选；不传时行为不变。这里不是话题标签筛选。
- **权限**：词表端点使用 `manage_dictionary`；标签列表、重命名、合并和热门统计使用 `manage_content`；标签下架与恢复使用 `review_content`。
- **前端影响**：词表管理页改从新列表读取并提供显式启用操作；标签管理页应区分“重命名”和“合并”，下架后不要清空帖子侧关系；复核页可按供应商审核标签筛选，热门榜只展示服务端返回的实时 TopN。
