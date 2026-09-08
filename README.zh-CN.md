# Placard

[English](README.md) · **简体中文**

自托管的 HTML 页面发布服务。从网页或 `placard` CLI 上传一个自包含的 `.html`
文件，拿到一条可以直接打开的分享链接。它适合那些由工具和 agent 生成、但没人愿意
当附件传来传去的报告、看板、幻灯片和一次性页面。

- **单个二进制，零依赖。** 开箱即用的 SQLite 与本地对象目录；Postgres、S3 兼容
  存储和 Redis 都是可选项。
- **完整的账号体系。** 本地密码（argon2id）加任意多个 OpenID Connect 提供方。
  第一个注册的人自动成为管理员。
- **页面带版本。** 原地重新发布：链接不变，历史版本可以列出、固定和回滚。
- **分享方式由你决定。** 私有或凭链接访问、可选过期时间、可选 6 位分享码、
  Open Graph 预览、浏览计数。
- **对象存储始终私有。** 页面字节一律经服务端代理，无需公开存储桶，全部同源提供。
- **配套 CLI 与 agent skill。** 在终端、CI 或编码 agent 里 `placard publish report.html`。

## 快速开始

### Docker

```bash
curl -fsSLO https://raw.githubusercontent.com/Xm798/Placard/HEAD/docker-compose.yaml
docker compose up -d
```

打开 <http://localhost:8080> 注册 —— 全新实例上创建的第一个账号即管理员，随后注册
自动关闭。

实例写入的一切都在 `placard_data` 卷里。挂到域名或反向代理后面时，把
`PLACARD_SERVER_BASE_URL` 设成浏览器实际访问的地址：分享链接由它拼出。

### 二进制

从 [Releases](https://github.com/Xm798/Placard/releases) 下载对应平台的压缩包
（服务端发布用不带前缀的 `vX.Y.Z` tag），然后：

```bash
tar xzf placard-server-*.tar.gz
./placard-server
```

它会创建 `./data/`（SQLite 数据库、对象、自动生成的实例密钥）并监听 `:8080`，
不需要任何配置文件。

### 从源码运行

需要 Go 1.26+ 与 Node 22+。

```bash
git clone https://github.com/Xm798/Placard.git
cd Placard
make frontend-install frontend   # 服务端内嵌的前端产物
make run                         # SQLite 落在 ./data，监听 :8080
```

## 用 CLI 发布

安装 CLI（单文件二进制，不需要 sudo，校验 sha256）：

```bash
# macOS / Linux
curl -fsSL https://your-placard.example.com/install.sh | sh

# Windows（PowerShell）
irm https://your-placard.example.com/install.ps1 | iex
```

每个实例都会提供这个安装脚本，它从本项目的 GitHub Releases（`cli/v*` tag）取二进制。
实例本身不可达时，直接从仓库安装：

```bash
curl -fsSL https://raw.githubusercontent.com/Xm798/Placard/HEAD/installer/install.sh | sh
```

然后登录并发布：

```bash
placard login --base https://your-placard.example.com
placard publish report.html
```

Placard 没有内置的默认服务端地址，所以第一次登录必须写明 `--base`，之后会被记住。
`login` 走设备码流程：终端打印一个 8 位授权码并尝试打开浏览器，你核对页面上的码与
终端一致后确认即可。

```
placard publish <file.html>   发布；加 --id <id> 则原地追加一个版本
placard ls                    列出自己的页面
placard rm <id>               删除页面
placard open <id>             在浏览器里打开分享链接
placard version ls|pin|restore  版本历史
placard whoami / login / logout / update
```

在 CI 里请到设置页创建 personal access token，并通过 `PLACARD_TOKEN` 环境变量传入，
不要用 `--token` 参数 —— 它会留在 shell 历史和 `ps` 输出里。

### 编码 agent

实例还会在 `/skill.md` 提供 agent skill，在 `/install.md` 提供它的安装说明。把
`https://your-placard.example.com/install.md` 交给 agent，它会自行安装 CLI、引导你
登录并完成配置。

## 配置

配置来自三处，优先级递增：内置默认值、可选的 YAML 文件、环境变量。任何标量键都可以
写成 `PLACARD_` 加上该键大写、点换成下划线的形式 —— `server.base_url` 对应
`PLACARD_SERVER_BASE_URL`。

配置文件的查找顺序是 `--config`、`$PLACARD_CONFIG`、当前目录下的 `config.yaml`。
[`config.example.yaml`](config.example.yaml) 是带注释的起点，
**[docs/configuration.md](docs/configuration.md) 列出了每个键、默认值与对应的环境变量**。

### 单点登录

任何提供 OpenID Connect discovery 文档的 provider 都能用，没有针对具体厂商的预设。
每配一项，登录页就多一个按钮。

```yaml
server:
  base_url: https://placard.example.com

auth:
  registration_open: false
  oidc:
    - name: corp
      display_name: Company SSO
      issuer: https://sso.example.com/realms/main
      client_id: placard
      client_secret: ${OIDC_CLIENT_SECRET}
      scopes: [openid, profile, email]
```

在 provider 那边把 `https://placard.example.com/auth/oidc/callback` 注册为回调地址。
`name` 会存在每条已关联的身份上，之后改名会让它们变成孤儿。

`auth.oidc` 里放的是对象而非字符串，是唯一无法用环境变量表达的键，所以启用 SSO 的
实例需要一个配置文件 —— 用 Docker 时把它放进 `/data/config.yaml`，服务端会自己找到。

> **启用 SSO 的实例请关闭本地注册。** provider 报告 `email_verified` 的 SSO 登录会
> 关联到同邮箱的已有账号，而本地注册填写的邮箱 Placard 并不验证。

### 数据库与存储

| | 默认 | 可选 |
| --- | --- | --- |
| 数据库 | `<data_dir>/placard.db` 的 SQLite | Postgres（`database.driver: postgres` 加 DSN） |
| 对象 | `<data_dir>/objects` 目录 | AWS S3、Cloudflare R2 或 MinIO（`storage.type: s3`） |
| 会话、限流、cron 锁 | 数据库与进程内 | Redis（`redis.addr`） |

数据库迁移在启动时执行，升级不需要手动跑 SQL。

**跑多副本必须配 Redis。** 否则每个副本各有一份会话、各有一份完整的限流额度、各跑
一份清理 cron —— 登录只在签发它的那个副本上有效。

## 管理

全新实例上的第一个账号一定是管理员，与 `auth.registration_open` 无关。管理员在
`/admin` 可以开关注册、切换 OIDC 自动开号、修改上传大小上限，以及授予、收回和禁用
账号。

当所有人都登不进去时 —— 忘了密码、管理员权限被收回、身份提供方下线 —— 服务端二进制
自带一个直接操作数据库的救援子命令：

```bash
placard-server admin user list
placard-server admin user create --username alice --email alice@example.com --admin
placard-server admin user set-admin --username alice
placard-server admin user reset-password --username alice
```

## 开发

```bash
make frontend-install frontend   # npm ci，再构建服务端内嵌的前端产物
make run                # 本地运行，SQLite 落在 ./data
make gates              # build + vet + gofmt + 单元测试，与 CI 一致
make up                 # 集成测试所需的 Postgres 与 Redis
make test-integration   # 同一批测试跑在 Postgres 与 Redis 上
```

单元测试不依赖任何外部服务：每个碰数据库的测试都有自己独立的 SQLite。

Go module path 是小写的 `github.com/Xm798/placard`，而仓库名是 `Xm798/Placard`。
import 路径和 `go install` 必须用小写那个：

```bash
go install github.com/Xm798/placard/cmd/placard@latest
```

## 许可

[MIT](LICENSE)
