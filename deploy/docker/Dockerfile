# 三个 target 对应三个镜像（D11）：
#   server  常驻 HTTP 服务
#   migrate 一次性迁移任务
#   jobs    由外部调度器触发的一次性后台任务
# 拆开是为了滚动更新时不会每个副本都想跑一次迁移。

# ---------- 依赖缓存层 ----------
FROM golang:1.26.7-alpine AS deps
WORKDIR /src
# 先只拷 go.mod/go.sum，让依赖层在源码变动时仍能命中缓存
COPY go.mod go.sum ./
RUN go mod download

# ---------- 构建层 ----------
FROM deps AS build
COPY . .
# CGO_ENABLED=0 才能用 distroless/static
# -trimpath 去掉构建机路径，-s -w 去掉符号表与调试信息
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/danshi-server ./cmd/danshi-server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/danshi-migrate ./cmd/danshi-migrate && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/danshi-jobs ./cmd/danshi-jobs

# ---------- server 镜像 ----------
# distroless/static：没有 shell、没有包管理器、没有 libc，攻击面最小。
# 时区靠 Go 的 time/tzdata（server main 里已 import），不依赖 /usr/share/zoneinfo。
FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/danshi-server /danshi-server
USER nonroot:nonroot
EXPOSE 8000
ENTRYPOINT ["/danshi-server"]

# ---------- migrate 镜像 ----------
# SQL 已 embed 进二进制，镜像里不需要额外文件——
# 这样也就不存在「镜像里的 SQL 和仓库里的不一致」。
FROM gcr.io/distroless/static-debian12:nonroot AS migrate
COPY --from=build /out/danshi-migrate /danshi-migrate
USER nonroot:nonroot
ENTRYPOINT ["/danshi-migrate"]

# ---------- jobs 镜像 ----------
FROM gcr.io/distroless/static-debian12:nonroot AS jobs
COPY --from=build /out/danshi-jobs /danshi-jobs
USER nonroot:nonroot
ENTRYPOINT ["/danshi-jobs"]
