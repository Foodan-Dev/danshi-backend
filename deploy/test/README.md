# 测试环境（danshi.test.fdueat.com）

部署在腾讯云 `150.158.39.107`，与生产 `danshi.fdueat.com` 同机、同一个 Traefik，
但**数据库与数据完全隔离**。

## 与生产的差异（务必知情）

| | 生产 | 测试 |
|---|---|---|
| `APP_PROFILE` | `prod` | `dev` |
| 验证码 | 腾讯云 SES 真发邮件 | **只写进 backend 日志**，不发信 |
| 内容审核 | 腾讯云数据万象 | **直接放行**，不调用供应商 |
| 数据库 | 外部实例 | 同机 `postgres:18` 容器，可随时重建 |
| 图床 | `danshi-img-1314208614` | `danshi-img-test-1314208614` |

选 `dev` profile 是有意的：测试服的目的是让团队点得动，
而不是复刻生产的外部依赖与配额。**因此它不能用来验证审核与发信链路**，
那两条要验证得单独配置真实凭证。

拿验证码的方式：

```bash
ssh tencent-cloud "docker logs danshi-test-backend-1 2>&1 | grep -i '验证码\|verification'"
```

## 路由

Traefik 按路径分派到同一个域名下的两个服务：

| 路径 | 服务 | 优先级 |
|---|---|---|
| `/api/**` | backend:8000 | 20 |
| 其余 | frontend:80 | 10 |

TLS 走既有的 `*.fdueat.com` 通配符证书，不需要单独签发。

## 部署

镜像在开发机构建后传到服务器（服务器只有 2 核 3G，不适合构建）：

```bash
# 后端
docker build -f deploy/docker/Dockerfile --target server \
  -t danshi-backend-go:test --build-arg VERSION=test-$(git rev-parse --short HEAD) .
docker build -f deploy/docker/Dockerfile --target migrate -t danshi-migrate-go:test .

# 前端（API 地址是构建期烘进 bundle 的，换地址必须重新构建）
cd ../Danshi-frontend
docker build -t danshi-frontend:test \
  --build-arg EXPO_PUBLIC_API_URL=https://danshi.test.fdueat.com/api/v2 .

# 传输
for img in danshi-backend-go:test danshi-migrate-go:test danshi-frontend:test; do
  docker save "$img" | ssh tencent-cloud "docker load"
done

# 起停
ssh tencent-cloud "cd ~/danshi-test-stack && docker compose up -d"
```

密钥在服务器本地 `~/danshi-test-stack/.env` 生成（`openssl rand`），不经过开发机。

## 已知坑

- **PostgreSQL 18 的挂载点变了**：要挂 `/var/lib/postgresql`，不是它下面的 `data`。
  挂错会直接报数据目录不兼容。
- **`danshi-migrate` 复用完整配置校验**，因此即使迁移用不到，
  也必须给它 `JWT_SECRET_KEY` 和 `EMAIL_VERIFICATION_SECRET`。
  值得改进：迁移工具应当只校验自己真正需要的配置项。
- **前端目前调不通接口**：现有前端针对 `/api/v1` 且 id 是字符串、price 是数字，
  与 `/api/v2` 契约不兼容。壳能打开，接口会失败。这是前端适配未做的必然结果，
  适配完成后重新构建镜像即可。
