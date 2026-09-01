# 图片上传与资产生命周期

旦食使用腾讯云 COS 作为对象存储，并通过客户端直传减少服务端带宽。精确请求与响应字段以 [OpenAPI](../api/openapi.json) 为准；本文记录必须跨客户端、服务端和运维共同遵守的协议。

## 1. 配置与可用性

图片链路依赖：

- `TENCENT_CLOUD_SECRET_ID`；
- `TENCENT_CLOUD_SECRET_KEY`；
- `COS_BUCKET`；
- `COS_REGION`；
- `COS_IMG_DOMAIN`；
- `COS_MAX_IMAGE_BYTES`；
- `COS_PRESIGN_TTL_SECONDS`；
- `COS_PRESIGN_GET_TTL_SECONDS`；
- `EDGEONE_ZONE_ID`。

图片审核还需要 `TENCENT_CI_BIZ_TYPE`、`TENCENT_CI_CALLBACK_URL` 和 `MODERATION_CALLBACK_TOKEN`。
server 内补审扫描间隔由 `IMAGE_MODERATION_RETRY_SCAN_INTERVAL_SECONDS` 控制，默认 30 秒。
server 内过期回收的扫描间隔与保留期分别由
`IMAGE_PENDING_EXPIRATION_SCAN_INTERVAL_SECONDS` 和
`IMAGE_PENDING_EXPIRATION_RETENTION_SECONDS` 控制，默认为 1 小时与 24 小时。

配置不完整时适配器 fail closed：不会签发伪造凭证或假定对象已存在，上传端点返回服务不可用。
生产 profile 在监听端口前用只读 `GetCIService` 探测 CI：401/403、`AccessDenied` 等鉴权或
权限错误以及 CI 未开通会拒绝启动；网络、限流与 5xx 只告警并继续，由持久补审兜底。

## 2. 客户端上传协议

流程固定为：

```text
POST presign → PUT COS → POST complete → 异步图片审核
```

`complete` 成功前，客户端不能把对象键或公开 URL 写入帖子/头像请求。

### 2.1 申请预签名凭证

`POST /api/v2/uploads/presign`，需要 access token。

| 字段 | 约束 |
|---|---|
| `purpose` | `post` 或 `avatar` |
| `content_type` | `image/jpeg`、`image/png`、`image/gif`、`image/webp`、`image/bmp`、`image/heic`、`image/heif` |
| `size` | 正整数，且不超过 `COS_MAX_IMAGE_BYTES` |
| `content_md5` | 必填；整个文件字节的 RFC 1864 Content-MD5 |

`content_md5` 是 16 字节 MD5 digest 的标准 Base64 编码，长度固定 24 字符并带 padding。它不是十六进制字符串，也不是 URL-safe Base64。

Python 计算示例：

```python
import base64
import hashlib

content_md5 = base64.b64encode(hashlib.md5(file_bytes).digest()).decode("ascii")
```

MD5 在这里是 COS 传输协议字段，用于把客户端声明签入 URL，不用于密码、认证或防攻击完整性决策。

成功响应包含：

- `upload_id`；
- `method`，恒为 `PUT`；
- `upload_url`；
- `expires_at`。

Presign 响应刻意不返回 `object_key` 和 `public_url`，避免客户端在对象尚未验证时提前引用可推导地址。

### 2.2 直传 COS

客户端向 `upload_url` 发送 PUT，Body 是原始图片字节。下列请求头必须与申请 presign 时完全一致，因为它们都参与签名：

- `Content-Type`；
- `Content-Length`；
- `Content-MD5`。

不要修改 URL 查询参数，不要在上传库中自动更换 Content-Type，也不要把文件转码后继续沿用原 size 和 MD5。

### 2.3 确认完成

`POST /api/v2/uploads/{upload_id}/complete`，需要上传者本人 access token。

服务端在持有资产行锁时：

1. 验证上传记录存在且属于当前用户；
2. 验证状态仍为 `pending` 且 `public_url` 为空（尚未 complete）；
3. 对 COS 执行 HEAD；
4. 验证对象存在、实际大小为正、未超过上限且等于申请值；
5. 构造配置域名下的公开 URL；
6. 写入公开 URL 和实际大小，引用状态继续保持 `pending`；
7. 提交图片审核任务。

第 7 步调用失败时，步骤 6 与一条补审记录仍在同一数据库事务提交，`complete` 照常成功，
图片保持 `moderation=pending`。补审 worker 使用有界指数退避再次提交；异步供应商受理后仍
等待回调写入最终结论。上传响应形状不因是否进入补审而改变。

成功响应包含 `upload_id`、`object_key`、`public_url` 和 `status=pending`。这里的
`pending` 表示尚未被业务内容引用，不表示对象上传未完成；上传完成由非空
`public_url` 表达。

如果实际大小不符，服务端删除 COS 对象并返回冲突。重复 complete 或已结束记录返回冲突；操作他人的上传记录返回 403。

### 2.4 私有图片读取

`GET /api/v2/uploads/{upload_id}` 只允许上传者本人读取自己的已完成、尚未回收图片，返回
短期 `image_url`；审核状态为 `pending`、`review`、`pass` 或 `block` 都不改变所有权。
`uploader_id` 因账号注销被置空后，本人路径 fail closed，不会把空值当成任意用户。

具备 `internal/authz` 既有内容审核能力的角色可以通过
`GET /api/v2/admin/images/{image_asset_id}` 查看单图详情；待人工复核队列中的图片和管理端
单用户详情中的头像也返回签名 URL。没有新增角色或图片专用权限概念。其他用户拿不到签名
URL，公开列表与详情仍返回 `PublicURL`，不改变既有内容可见性筛选。

读取签名由 `COS_PRESIGN_GET_TTL_SECONDS` 控制，默认 3600 秒。1 小时足以覆盖审核员在队列
页面翻页和裁决的常见停留时间，又把 URL 泄露后的无鉴权访问窗口限制在一个较短班次内；
页面长时间停留超过有效期时应重新请求，不应缓存或持久化完整签名 URL。

## 3. 业务引用

### 3.1 帖子图片

- 单帖最多 9 张；
- 客户端通过 `public_url` 提交，服务端反查真实 `image_assets`；
- 每张图必须已 complete、尚未被回收、属于帖子作者且用途为 `post`；
- `post_images.position` 从 0 开始保序；
- 数据库真正保存 `image_asset_id` 外键，不在帖子中保存 URL 副本。

帖子允许在图片审核仍为 `pending/review` 时建立引用；插入 `post_images` 后，数据库
触发器把资产激活为 `ready`，帖子本身则保持 `pending`，直到文本与全部图片均为
`pass` 才能公开。

### 3.2 头像

- 注册不能设置头像；
- 资料更新时可提交头像公开 URL；
- 服务端反查资产并要求图片已 complete、未被回收、上传者是本人、用途为
  `avatar`，且审核状态已经是 `pass`；
- `users.avatar_image_asset_id` 保存外键，读取时 join 得到公开 URL；
- 审核仍为 `pending/review` 时换绑返回 `image_not_approved`，当前旧头像保持不变；
- 绑定成功后，数据库触发器把新头像资产激活为 `ready`；
- 解除最后引用后旧头像资产退役。

### 3.3 审核与发布

资产 `status=ready` 表示当前存在业务引用，不等于内容审核通过。审核状态独立记录。

允许帖子在图片审核进行中建立引用，以免异步审核阻断表单提交；但帖子进入 `approved` 前必须在同一事务确认所有引用图片审核通过。

图片审核回调携带资产 ID，服务端按供应商任务号幂等写入审核记录，并重新评估引用该图片的待审帖子。审核终态和一条 `image_access_intents` 在同一个 UoW 事务中提交；outbox 写入失败会让审核事务整体回滚，不会出现“数据库已经 block、访问状态意图却丢失”。`image_assets.moderation` 仍是审核真源，`image_access_deliveries` 只投影最新期望并保存交付进度。

管理员下架帖子也复用同一 durable 访问收敛链路。服务端先锁帖子，再按资产 ID 升序锁定全部
附图，软删除后按“帖子未软删除”的有效引用口径检查反向引用，不要求引用帖已经审核通过。
仍有其他未软删除帖子（包括待审核帖子）引用的共享图保持公开；已无未软删除帖子引用的图片追加
`provider=admin_post_delete`、`verdict=block` 的独立审核流水（记录 reviewer，不设置
`supersedes_id`），写回当前审核状态，并在同一事务写入 `desired_public=false`、
`purge_required=true` 的 intent。这样共享图不会被误伤，最后一条未软删除引用消失后也不会漏收紧。

每条 intent 以 `source_moderation_record_id` 幂等，每张图片只有一条 delivery。新的审核事实即使目标状态相同也会提升 generation、从 ACL 重新校准；同一供应商任务的重复回调不提升 generation，避免重放攻击不断重置 worker。反向状态通过 generation fencing 覆盖旧意图，旧 worker 的 finalize 会影响 0 行并释放旧 lease，随后新代际收敛。delivery 的 `purge_required` 由审核前状态决定：转私有以及从 `review`/`block` 恢复公开时为 true，首次 `pending`→`pass` 为 false；intent 无论是否需要刷新都必须写入。

运行时通过腾讯云官方 Go SDK 调用 `CreatePurgeTask`，类型固定为 `purge_url`。适配器只接受 `COS_IMG_DOMAIN` 下不带查询参数的单个 HTTPS 原图 URL，再把 API 实际暴露的 `raw`、`display`、`thumb` 三个**精确 URL**作为同一任务的 Targets；它不提供目录、Hostname、全站、Cache-Tag 或跨站点刷新能力。这样既清除原图缓存，也覆盖 EdgeOne 完整 URL Cache Key 下彼此独立的两档数据万象派生图。

外部调用只由一次性命令 `danshi-jobs reconcile-image-access -batch-size 4` 执行。领取使用一个短事务内的 `FOR UPDATE SKIP LOCKED` 与 60 秒 lease；COS/EdgeOne 网络调用从不占用数据库事务。所有 finalize 都以 `image_asset_id + generation + lease_token` fencing。默认并发 batch 为 4、硬上限 4，保证单项 15 秒 ACL 与 12 秒 EdgeOne 硬超时不会因批内排队跨过 lease；调用方应由 cron/CronJob 高频触发，直到 backlog 收敛。

worker 先幂等设置 COS ACL；若 `purge_required=false`，ACL 成功后直接收敛为 `succeeded`，不进入 `pending_submit`。需要刷新时，worker 才在单独事务中把状态持久化为 `submitting`，之后调用 `CreatePurgeTask`。Create 没有 ClientToken，SDK 三种自动重试全部关闭；EOF、超时、连接中断、空响应或进程在写回 JobId 前崩溃时，租约恢复只通过 `DescribePurgeTasks` 的窄时间窗与精确 Target 集合对账，绝不直接重放 Create。找不到唯一因果任务时在 90 秒观察窗后进入 dead-letter；只有已知 JobId 明确返回 `failed/timeout/canceled`，或腾讯结构化拒绝经对账确认未受理，才按最多 3 个 submission 的预算重试。

取得 JobId 后，worker 必须使用 `AdvancedFilter{Name:"job-id", Fuzzy:false}` 分页查询。一个 Job 会为 raw/display/thumb 各返回一条 Task；三个精确 Target 全部 `success` 才算完成，任一 `failed/timeout/canceled` 是失败终态，`processing` 或缺行继续有界轮询，超过 10 分钟按失败处理。单进程 Create/Describe 共用 burst=1、10 QPS gate，低于两个官方接口各 20 QPS 的默认限额；多副本仍需按账号总量控制。

运营状态为 `pending_acl`、`pending_submit`、`submitting`、`submitted`、`succeeded`、`dead_letter`。ACL 最多 8 次；查询失败最多 8 次；退避是确定性的指数增长并有上限。`last_error_code` 只允许 migration 白名单中的低基数内部枚举，不保存供应商正文、RequestId、URL、对象键或 JobId；日志与 Prometheus label 同样不包含这些载荷。dead-letter 不靠重复 callback 偶然复活，应先人工确认 EdgeOne 实际任务与对象状态，再用受审计的显式新审核事实或后续专用 requeue 工具处理。

## 4. 生命周期

| 状态 | 含义 |
|---|---|
| `pending` | 尚未被任何帖子或头像引用；可能仍在直传，也可能已 complete 等待引用 |
| `ready` | 当前至少存在一条帖子或头像引用 |
| `retired` | 曾被引用后已解除全部引用，或无引用对象已被回收；数据库行保留 |

上传完成与否不再复用 `status` 表达：`public_url=''` 表示尚未 complete，非空 HTTPS
URL 表示对象已经验证。回收已删除对象时，原公开 URL 会替换成唯一的内部墓碑 URN，
避免并发等待中的引用写入继续匹配一个已经不存在的对象。

图片行不走普通 DELETE。对象与数据库记录的清理顺序必须显式控制。

### 4.1 引用激活与退役

数据库触发器负责：

- 新增 `post_images` 引用时激活资产；
- 绑定新头像时激活资产；
- 删除帖子图片的最后引用时退役资产；
- 头像换绑后，在无其他引用时退役旧资产。

引用变化前，service 必须锁定涉及的 `image_assets` 行。多资产操作按 ID 升序锁定，所有发帖、编辑、删帖、恢复和头像换绑路径使用同一顺序。

管理员下架与作者删除的关联处理不同：作者删除会解除图片关联并由上述触发器处理资产状态；
管理员下架保留关联供审计，只通过未软删除帖子反向引用判断是否需要把图片访问收紧。管理员下架使用
`deleted_reason=admin`，不属于现有只允许 `deleted_reason=moderation` 的误杀恢复范围。

没有这个服务层协议时，“删除最后引用”和“并发新增引用”可能各自基于不同事务快照，最终出现仍有引用但资产已退役。

### 4.2 过期 pending

用户申请 presign 后可能不上传、不调用 complete，或 complete 后始终没有建立业务引用。
这些行都会保持 `pending`，回收流程为：

1. 按创建时间查找超时 pending；
2. `FOR UPDATE SKIP LOCKED` 锁定一批记录；
3. 再次确认没有帖子或头像引用；
4. 删除对应 COS 对象，删除操作幂等；
5. 把数据库状态设为 `retired`，并把失效公开 URL 替换为唯一墓碑 URN。

`SKIP LOCKED` 避免与正在执行 complete 的事务争用，从而防止对象刚被 complete 验证又被清理任务删除。

server 进程启动后会立即运行一个有界批次，之后按配置的间隔周期扫描。
默认保留 24 小时：这远长于 10 分钟的预签名有效期，为选图、裁剪、网络重试和暂时离开编辑流程留出宽裕时间；
默认每小时扫描则把实际回收时间控制在约 24–25 小时，又不会频繁轮询数据库。
空批次不记日志。

仓库仍保留一次性命令 `danshi-jobs expire-pending`，供运维用不同保留期临时执行。
命令的过期时长没有默认值，调用方必须显式给出；先 dry-run：

```bash
danshi-jobs expire-pending -older-than 24h -batch-size 100 -dry-run
danshi-jobs expire-pending -older-than 24h -batch-size 100
```

命令每次只处理一个有界批次。多个实例并发运行时，`FOR UPDATE SKIP LOCKED` 会把
候选分片，避免重复删除；单个 COS 删除失败会保留该行 `pending` 并继续处理同批其他
对象。结束时结构化日志输出 `selected`、`retired`、`failed`、`dry_run` 与耗时；存在
删除失败时，成功项仍提交，进程以非零状态退出，供外部调度器告警和重试。

## 5. 物理清理

数据库触发器默认拒绝 `DELETE FROM image_assets`。真正物理删除前必须确认：

- 没有帖子或头像引用；
- 对象存储文件已经删除或明确不再需要；
- 操作范围经过审阅；
- 同一事务内使用专用开关。

事务方式：

```sql
BEGIN;
SET LOCAL danshi.allow_image_asset_delete = 'on';
DELETE FROM image_assets WHERE id = 123;
COMMIT;
```

或调用 schema 提供的封装函数：

```sql
SELECT danshi_purge_image_assets(ARRAY[123, 456]::bigint[]);
```

`SET LOCAL` 必须与 DELETE 在同一事务，不能拆成 autocommit 下的两条命令。开关只绕过图片行禁删触发器，不绕过外键。

## 6. 故障语义

| 情况 | 行为 |
|---|---|
| COS 未配置或不可用 | presign、HEAD 等存储操作返回 503，fail closed |
| presign 输入格式错误 | 422，字段级错误 |
| upload 不存在 | 404 |
| upload 不属于当前用户 | 403 |
| upload 已完成或已退役 | 409 |
| COS 中没有对象 | 409 |
| 实际大小不匹配 | 删除对象并返回 409 |
| 图片送审调用失败 | complete 成功并登记补审，图片保持 pending、不可随帖子公开 |
| 图片补审耗尽 8 次预算 | 保留 dead-letter，写错误日志并暴露固定状态指标 |
| `review/block` 后 COS ACL 更新失败 | 审核事实与 intent 已提交；worker 有界退避，耗尽后 dead-letter |
| Create 响应未知或进程崩溃 | 保持 `submitting`，只读对账；不盲目重放 |
| EdgeOne 长期 processing/失败终态 | 有界轮询；明确失败按 submission 预算重试，耗尽后 dead-letter |
| EdgeOne 未配置或权限不足 | jobs 命令 fail closed；delivery 保留或进入可观测 dead-letter |

客户端不能根据中文文案分支，应使用 HTTP 状态和稳定 `error_code`。

## 7. 安全要求

- 云凭据只在服务端配置，绝不下发客户端；
- 预签名 URL 有短 TTL；
- 对象 key 由服务端使用用户 ID、年月和随机 128 bit 片段生成；
- Content-Type、Content-Length 和 Content-MD5 全部签入 URL；
- 公开 URL 只通过配置的 HTTPS 域名构造；
- `COS_IMG_DOMAIN` 必须是只含 scheme 与 host 的 HTTPS origin，EdgeOne 站点 ID 不接受 `*`；
- 回调 URL 使用 HTTPS，并带独立的高强度 callback token；
- 回调按 provider job ID 幂等；
- `review/block` 图片对象使用可逆的私有 ACL，改判 `pass` 时重新公开；
- 私有图片读取签名默认 1 小时有效，完整签名 URL 不进入日志、trace 或持久缓存；
- 运行身份除上传、HEAD 和清理权限外，还必须具有目标对象的 ACL 修改权限；
- EdgeOne 只授予目标站点的 `teo:CreatePurgeTask` 与 `teo:DescribePurgeTasks`，资源限定为
  `qcs::teo::uin/<主账号 UIN>:zone/<EDGEONE_ZONE_ID>`，不得授予 `teo:*` 或全站点资源；
- 用户只能引用本人、用途匹配的资产；
- trace 与日志不得记录云密钥、完整预签名 URL 或 callback token。

EdgeOne jobs 运行身份的最小 CAM 语句为（替换主账号 UIN 与真实 Zone ID；测试、生产分别授权）：

```json
{
  "effect": "allow",
  "action": [
    "name/teo:CreatePurgeTask",
    "name/teo:DescribePurgeTasks"
  ],
  "resource": [
    "qcs::teo::uin/<主账号 UIN>:zone/<EDGEONE_ZONE_ID>"
  ]
}
```

Server 进程只写数据库 outbox，不需要调用这两个 TEO action；若 server 与 jobs 复用同一
CAM 子账号，这是部署简化而不是应用最小权限要求。`DescribePurgeTasks` 是终态确认和
response-unknown 对账的必需权限，不能只授予 Create。
