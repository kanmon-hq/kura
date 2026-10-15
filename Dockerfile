# ==========================================
# 1. Build Stage
# ==========================================
FROM golang:1.27.2-alpine AS builder

WORKDIR /app

# CA証明書とビルドツール
RUN apk add --no-cache ca-certificates git

# 依存関係のキャッシュ
COPY go.mod go.sum* ./
RUN go mod download || true

# ソースコードコピー & ビルド
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /app/bin/server ./cmd/server

# ==========================================
# 2. Runtime Stage (Minimal Alpine)
# ==========================================
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata

# 非特権ユーザーで実行
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /app/bin/server /app/server

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/livez || exit 1

ENTRYPOINT ["/app/server"]
