# SQL Proxy Docker 部署指南

本项目已配置为 Docker 容器运行。

## 快速开始

### 方法 1：使用 Docker Compose（推荐）

```bash
# 构建镜像
docker-compose build

# 启动容器
docker-compose up -d

# 查看日志
docker-compose logs -f sql-proxy

# 停止容器
docker-compose down
```

### 方法 2：直接使用 Docker 命令

```bash
# 构建镜像
docker build -t sql-proxy:latest .

# 运行容器
docker run -d \
  --name sql-proxy \
  -p 3306:3306 \
  -v $(pwd)/config.yaml:/app/config.yaml \
  sql-proxy:latest

# 查看日志
docker logs -f sql-proxy

# 停止容器
docker stop sql-proxy
docker rm sql-proxy
```

## 配置

### 通过配置文件配置

修改 `config.yaml` 中的参数：

```yaml
mysql_server:
  addr: "0.0.0.0:3306"  # 容器内需要改为 0.0.0.0 以接收外部连接
  user: "root"
  password: "your_password"

backend:
  base_url: "https://your-api-url"
  username: "your_username"
  password: "your_password"
  timeout: 30
  max_retries: 3

log:
  level: "info"
  format: "text"
```

### 环境变量配置

在 `docker-compose.yml` 中添加环境变量，或使用 `docker run -e` 选项。

## 健康检查

容器包含健康检查，自动监测 MySQL 代理端口 3306 的连接状态。

```bash
# 检查容器状态
docker-compose ps
# 或
docker ps -a | grep sql-proxy
```

## 常见问题

### 1. 容器无法连接到后端服务
- 确保 `config.yaml` 中的 `backend.base_url` 正确
- 检查容器网络配置和防火墙

### 2. MySQL 客户端无法连接到代理
- 在 `docker run` 时确保使用 `-p 3306:3306` 映射端口
- 修改 `config.yaml` 中的 MySQL 地址为 `0.0.0.0:3306`（而不是 127.0.0.1）

### 3. 查看容器日志
```bash
docker-compose logs -f sql-proxy
```

## 镜像优化

Dockerfile 使用多阶段构建：
- **构建阶段**：使用 golang:1.21-alpine 编译应用
- **运行阶段**：使用 alpine:latest 轻量级镜像运行，减小最终镜像大小

最终镜像大小通常在 20-30MB 左右。

## 生产部署建议

1. **安全性**：
   - 避免在 `config.yaml` 中存储敏感信息
   - 使用 Docker secrets 或环境变量管理敏感数据
   - 使用私有镜像仓库

2. **资源限制**：
   ```yaml
   services:
     sql-proxy:
       # ... 其他配置
       deploy:
         resources:
           limits:
             cpus: '1'
             memory: 512M
           reservations:
             cpus: '0.5'
             memory: 256M
   ```
