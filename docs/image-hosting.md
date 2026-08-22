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
- `COS_PRESIGN_TTL_SECONDS`。

图片审核还需要 `TENCENT_CI_BIZ_TYPE`、`TENCENT_CI_CALLBACK_URL` 和 `MODERATION_CALLBACK_TOKEN`。

配置不完整时适配器 fail closed：不会签发伪造凭证或假定对象已存在，上传端点返回服务不可用。

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
2. 验证状态仍为 `pending`；
3. 对 COS 执行 HEAD；
4. 验证对象存在、实际大小为正、未超过上限且等于申请值；
5. 构造配置域名下的公开 URL；
6. 把资产标记为 `ready`；
7. 提交图片审核任务。

成功响应包含 `upload_id`、`object_key`、`public_url` 和 `status=ready`。

如果实际大小不符，服务端删除 COS 对象并返回冲突。重复 complete 或已结束记录返回冲突；操作他人的上传记录返回 403。

## 3. 业务引用

### 3.1 帖子图片

- 单帖最多 9 张；
- 客户端通过 `public_url` 提交，服务端反查真实 `image_assets`；
- 每张图必须属于帖子作者、用途为 `post`、状态为 `ready`；
- `post_images.position` 从 0 开始保序；
- 数据库真正保存 `image_asset_id` 外键，不在帖子中保存 URL 副本。

### 3.2 头像

- 注册不能设置头像；
- 资料更新时可提交头像公开 URL；
- 服务端反查资产并要求上传者是本人、用途为 `avatar`、状态为 `ready`；
- `users.avatar_image_asset_id` 保存外键，读取时 join 得到公开 URL；
- 解除最后引用后旧头像资产退役。

### 3.3 审核与发布

资产 `status=ready` 表示对象上传完成，不等于内容审核通过。审核状态独立记录。

允许帖子在图片审核进行中建立引用，以免异步审核阻断表单提交；但帖子进入 `approved` 前必须在同一事务确认所有引用图片审核通过。

图片审核回调携带资产 ID，服务端按供应商任务号幂等写入审核记录，并重新评估引用该图片的待审帖子。

## 4. 生命周期

| 状态 | 含义 |
|---|---|
| `pending` | 已创建上传记录，尚未完成对象验证 |
| `ready` | 对象存在且大小验证通过，可建立用途匹配的引用 |
| `retired` | 不再被使用或上传已过期；数据库行保留 |

图片行不走普通 DELETE。对象与数据库记录的清理顺序必须显式控制。

### 4.1 引用激活与退役

数据库触发器负责：

- 新增 `post_images` 引用时激活资产；
- 删除帖子图片的最后引用时退役资产；
- 头像换绑后，在无其他引用时退役旧资产。

引用变化前，service 必须锁定涉及的 `image_assets` 行。多资产操作按 ID 升序锁定，所有发帖、编辑、删帖、恢复和头像换绑路径使用同一顺序。

没有这个服务层协议时，“删除最后引用”和“并发新增引用”可能各自基于不同事务快照，最终出现仍有引用但资产已退役。

### 4.2 过期 pending

用户申请 presign 后可能不上传或不调用 complete。回收流程：

1. 按创建时间查找超时 pending；
2. `FOR UPDATE SKIP LOCKED` 锁定一批记录；
3. 删除对应 COS 对象，删除操作幂等；
4. 把数据库状态设为 `retired`。

`SKIP LOCKED` 避免与正在执行 complete 的事务争用，从而防止对象刚被 complete 验证又被清理任务删除。

当前仓库提供 `UploadService.ExpirePending` 领域能力，但没有独立的定时清理命令或调度配置。部署方在接入调度前必须提供可观测、可重试且有 dry-run/批次边界的入口，不能把旧运行时命令写进操作手册。

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
| COS/CI 未配置或不可用 | 503，fail closed |
| presign 输入格式错误 | 422，字段级错误 |
| upload 不存在 | 404 |
| upload 不属于当前用户 | 403 |
| upload 已完成或已退役 | 409 |
| COS 中没有对象 | 409 |
| 实际大小不匹配 | 删除对象并返回 409 |
| 审核供应商失败 | 不宣告完整完成，保留可追踪错误原因 |

客户端不能根据中文文案分支，应使用 HTTP 状态和稳定 `error_code`。

## 7. 安全要求

- 云凭据只在服务端配置，绝不下发客户端；
- 预签名 URL 有短 TTL；
- 对象 key 由服务端使用用户 ID、年月和随机 128 bit 片段生成；
- Content-Type、Content-Length 和 Content-MD5 全部签入 URL；
- 公开 URL 只通过配置的 HTTPS 域名构造；
- 回调 URL 使用 HTTPS，并带独立的高强度 callback token；
- 回调按 provider job ID 幂等；
- 用户只能引用本人、用途匹配的资产；
- trace 与日志不得记录云密钥、完整预签名 URL 或 callback token。
