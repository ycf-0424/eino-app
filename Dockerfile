# 第一阶段只负责编译。先复制依赖清单可以复用 Docker 构建缓存。
FROM golang:1.26.3-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/eino-server ./cmd/server \
    && go build -trimpath -ldflags="-s -w" -o /out/session-migrate ./cmd/session-migrate \
    && go build -trimpath -ldflags="-s -w" -o /out/user-admin ./cmd/user-admin \
    && go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer \
    && go build -trimpath -ldflags="-s -w" -o /out/memory-reindex ./cmd/memory-reindex

# 运行镜像不包含 Go 编译器，减少镜像体积和攻击面。
FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata clamav poppler-utils ffmpeg \
    && addgroup -S eino \
    && adduser -S -G eino eino

WORKDIR /app
COPY --from=builder /out/eino-server ./eino-server
COPY --from=builder /out/session-migrate ./session-migrate
COPY --from=builder /out/user-admin ./user-admin
COPY --from=builder /out/indexer ./indexer
COPY --from=builder /out/memory-reindex ./memory-reindex
COPY config.docker.yaml ./config.yaml
COPY skills ./skills
COPY docs/knowledge ./docs/knowledge
COPY workspace-files ./workspace-files

# Session、Checkpoint 和工具输出都写入 /app/data，由 Compose 命名卷持久化。
RUN mkdir -p /app/data/sessions /app/data/checkpoints /app/data/notes /app/data/documents /app/data/skills /app/data/templates \
    && chown -R eino:eino /app

USER eino
EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=60s --retries=5 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["./eino-server"]
CMD ["-addr", ":8080"]
