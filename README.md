# sql-proxy

`sql-proxy` 是一个将 MySQL 客户端请求转换为 sqlpara 平台请求的轻量级代理。启动后，DBeaver、DataGrip、MySQL CLI 等支持 MySQL 协议的客户端可以像连接 MySQL 一样连接它，SQL 会通过 sqlpara 执行并以 MySQL 结果集格式返回。

## 工作方式

```text
MySQL 客户端
			|
			| MySQL 协议
			v
sql-proxy :3306
			|
			| HTTP/HTTPS，自动登录 sqlpara
			v
sqlpara 平台
```

代理本身不直接连接数据库。它会：

- 在本地监听 MySQL 协议端口并校验客户端账号密码；
- 自动访问 sqlpara 登录页并使用配置的账号登录；
- 将 SQL、当前数据库和实例名提交到 sqlpara；
- 将 sqlpara 返回的数据转换为 MySQL 客户端可识别的结果集；
- 对后端请求执行自动重试，并在认证失效时重新登录。

## 环境要求

- Go `1.25` 或更高版本；
- 可访问 sqlpara 平台；
- 一个有效的 sqlpara 账号；
- 若使用 Docker，则需要 Docker Compose。

## 快速开始

### 1. 准备配置

复制示例配置并按实际环境修改：

```bash
cp config-example.yaml config.yaml
```

Windows PowerShell：

```powershell
Copy-Item config-example.yaml config.yaml
```

至少需要设置 `backend.base_url`、`backend.username` 和 `backend.password`。`mysql_server.user` 与 `mysql_server.password` 是客户端连接代理时使用的账号密码，与 sqlpara 登录账号可以不同。

### 2. 启动代理

直接运行：

```bash
go run ./cmd/sql-proxy -config config.yaml
```

构建后运行：

```bash
go build -o sql-proxy ./cmd/sql-proxy
./sql-proxy -config config.yaml
```

程序默认读取当前目录下的 `config.yaml`，也可以通过 `-config` 指定其他路径。

### 3. 使用 MySQL 客户端连接

```bash
mysql -h 127.0.0.1 -P 3306 -u root -p
```

连接后可以执行：

```sql
SHOW DATABASES;
USE instance_name/database_name;
SELECT * FROM your_table;
```

其中 `instance_name` 是 sqlpara 中的实例名，`database_name` 是目标数据库名。也可以只执行 `USE database_name`，此时仅切换当前数据库。

## 配置说明

完整示例见 [`config-example.yaml`](config-example.yaml)。

```yaml
mysql_server:
    addr: "127.0.0.1:3306"
    version: "5.7.0"
    auth_method: "mysql_native_password"
    user: "root"
    password: "change-me"

backend:
    base_url: "https://sqlpara.example.com"
    proxy_url: ""
    username: "sqlpara-user"
    password: "change-me"
    timeout: 30
    max_retries: 3
    headers: {}

log:
    level: "info"
    format: "text"
```

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `mysql_server.addr` | `127.0.0.1:3306` | 代理监听地址 |
| `mysql_server.version` | `5.7.0` | 对客户端声明的 MySQL 版本 |
| `mysql_server.auth_method` | `mysql_native_password` | 客户端认证方式 |
| `mysql_server.user` | `root` | 客户端连接账号 |
| `mysql_server.password` | 空字符串 | 客户端连接密码 |
| `backend.base_url` | `https://sqlpara.example.com` | sqlpara 平台基础地址 |
| `backend.proxy_url` | 空字符串 | 访问 sqlpara 时使用的 HTTP 代理 |
| `backend.username` | 未设置 | sqlpara 登录账号 |
| `backend.password` | 未设置 | sqlpara 登录密码 |
| `backend.timeout` | `30` | HTTP 请求超时时间，单位为秒 |
| `backend.max_retries` | `3` | SQL 请求失败后的最大重试次数 |
| `backend.headers` | 空 | 额外 HTTP 请求头 |
| `log.level` | `info` | `panic`、`fatal`、`error`、`warn`、`info`、`debug` 或 `trace` |
| `log.format` | `text` | `text` 或 `json` |

## Docker 部署

编辑 `config.yaml` 后执行：

```bash
docker compose up -d --build
```

查看日志：

```bash
docker compose logs -f sql-proxy
```

停止服务：

```bash
docker compose down
```

默认将容器的 `3306` 端口映射到宿主机的 `3306` 端口，并挂载当前目录的 `config.yaml` 到容器内 `/app/config.yaml`。如宿主机的 `3306` 已被占用，可在 [`docker-compose.yml`](docker-compose.yml) 中改为例如 `13306:3306`，然后使用 `-P 13306` 连接。

## 支持范围

当前代理重点支持以下场景：

- 普通 SQL 查询及结果集返回；
- `SELECT`、`INSERT`、`UPDATE`、`DELETE` 等请求转发；
- `SHOW DATABASES`、`SHOW TABLES`、`SHOW COLUMNS`、`DESCRIBE`；
- MySQL 预处理语句的基本执行流程；
- `USE`、`SET`、`BEGIN`、`COMMIT`、`ROLLBACK` 的基础兼容处理；
- `SHOW VARIABLES`、`SHOW STATUS`、`SELECT VERSION()` 的兼容响应。

以下行为需要注意：

- 代理只负责协议转换，不提供数据库存储能力；
- 未特别处理的 SQL 可能获得空的成功响应，不代表 SQL 已在后端执行；
- `mysql.user` 相关查询会返回空成功响应；
- MySQL Field List 命令和其他未实现协议命令会返回“不支持”错误；
- 配置文件包含账号密码，请勿提交真实凭据到版本库，生产环境应限制配置文件权限。

## 日志与排障

将日志级别调整为 `debug` 可以查看认证、重试和后端请求信息：

```yaml
log:
	level: "debug"
	format: "text"
```

常见问题：

1. **无法启动：端口被占用**：修改 `mysql_server.addr`，或停止占用该端口的进程。
2. **认证失败**：分别确认 MySQL 客户端账号密码和 sqlpara 账号密码；两者属于不同认证层。
3. **SQL 执行失败**：确认 `backend.base_url` 可访问，并检查 `USE instance/database` 中的实例名和数据库名。
4. **需要经过网络代理访问 sqlpara**：设置 `backend.proxy_url`，例如 `http://127.0.0.1:7890`。

## 项目结构

```text
cmd/sql-proxy/       程序入口
internal/config/     YAML 配置加载与日志初始化
internal/proxy/      MySQL 协议服务与请求处理
internal/backend/    sqlpara HTTP 客户端与响应转换
config-example.yaml  配置示例
Dockerfile           Docker 镜像构建文件
docker-compose.yml   Docker Compose 配置
```
