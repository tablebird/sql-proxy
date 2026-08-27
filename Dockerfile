# 构建阶段
FROM golang:1.25-alpine AS builder

# 安装必要的构建工具
RUN apk add --no-cache git ca-certificates

# 设置工作目录
WORKDIR /app

# 复制go mod文件
COPY go.mod go.sum* ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o sql-proxy ./cmd/sql-proxy

RUN go version -m sql-proxy

# 运行阶段
FROM alpine:latest

# 安装运行时依赖（如果需要）
RUN apk add --no-cache ca-certificates

# 设置工作目录
WORKDIR /app

# 从builder阶段复制构建的二进制文件
COPY --from=builder /app/sql-proxy .

# 复制配置文件
COPY config-example.yaml config.yaml

# 暴露MySQL代理端口
EXPOSE 3306

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD nc -z 127.0.0.1 3306 || exit 1

# 启动应用
CMD ["./sql-proxy", "-config", "config.yaml"]
