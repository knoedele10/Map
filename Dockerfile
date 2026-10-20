# syntax=docker/dockerfile:1

# ---------- 构建阶段 ----------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata

# 与 backend/configs/config.dev.yaml 里的相对路径保持一致：
#   ../data/...     -> /app/data/...
#   ../frontend     -> /app/frontend
#   configs/...     -> /app/backend/configs
WORKDIR /app/backend

COPY --from=build /out/server ./server
COPY backend/configs/config.dev.yaml ./configs/config.yaml
COPY backend/configs/sites.json ./configs/sites.json
COPY backend/openapi.yaml ./openapi.yaml
COPY frontend /app/frontend
COPY data/corpus /app/data/corpus

RUN mkdir -p /app/data/runtime/data/assets

# 监听 0.0.0.0，Ollama 默认用宿主机上的服务
ENV RAG_SERVER_ADDR=0.0.0.0:8090 \
    RAG_STORAGE_SITES_FILE=configs/sites.json \
    RAG_EMBEDDING_BASE_URL=http://host.docker.internal:11434 \
    RAG_CHAT_BASE_URL=http://host.docker.internal:11434 \
    RAG_VISION_BASE_URL=http://host.docker.internal:11434

EXPOSE 8090

# 索引、上传的图片都写在这里，挂卷持久化
VOLUME ["/app/data/runtime"]

CMD ["./server"]
