# seltool-go

PT 站点保活工具。用无头浏览器定期访问站点，注入 CookieCloud 的登录凭据，
保持账号活跃；失败按原因分组推送通知。

本仓库是 [linzaixian888/selenium-tool](https://github.com/linzaixian888/selenium-tool)
（Java 17 + Spring Boot + Selenium）的 **Go 重写版**，保留全部用户可见功能，
未使用其源代码。单容器部署，带 Web 面板和 noVNC 实时观看。

## 特性

- **多站点保活** — 每个站点独立标签页，注入 Cookie 后访问，失败自动刷新凭据重试一次
- **登录态判定** — NexusPHP 标记 → SPA 登录入口 → 可见密码框 → 标题关键字，四层兜底
- **失败原因具体化** — 区分「Cookie 过期 / Token 过期 / 人机验证拦截 / 域名解析失败 / 站点故障」，各给对应处理动作
- **CookieCloud 集成** — 每天自动同步，新站点可自动加入目标列表
- **MoviePilot 同步** — 站点名和图标从 MoviePilot 拉取，每天 05:00
- **Web 面板** — 目标管理、搜索、批量导入、8 套主题、noVNC 实时观看
- **多架构镜像** — amd64 / arm64，GitHub Actions 自动构建

## 快速开始

### 用镜像（推荐）

```sh
# 多架构清单，Docker 自动选本机架构
docker pull ghcr.io/a454735639-lab/seltool-go:latest
```

离线环境从 [Releases](https://github.com/a454735639-lab/seltool-go/releases) 下载对应架构的
`seltool-go-amd64.tar.gz` / `seltool-go-arm64.tar.gz`，`docker load` 导入。

### 准备配置

```sh
mkdir -p /opt/seltool && cd /opt/seltool
mkdir -p config data

# 从仓库取两个模板
curl -fsSLO https://raw.githubusercontent.com/a454735639-lab/seltool-go/master/deploy/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/a454735639-lab/seltool-go/master/.env.example
mv .env.example .env

# 改这两项，其余可留默认
#   IMAGE            镜像地址
#   SE_VNC_PASSWORD  noVNC 密码（必改）
vi .env
```

### 启动

```sh
docker compose up -d
```

打开 `http://<你的IP>:8081`，首次进入注册管理员账号。

noVNC 实时观看（仅本机）：`http://127.0.0.1:7900`

### 配置文件内容

不想下载模板的话，直接照抄这两份。

****

```yaml
services:
  seltool:
    image: ${IMAGE:?请在 .env 里设置 IMAGE}
    container_name: seltool-go
    restart: unless-stopped
    environment:
      TZ: ${TZ:-Asia/Shanghai}
      SE_VNC_PASSWORD: ${SE_VNC_PASSWORD:?请在 .env 里设置 SE_VNC_PASSWORD}
      VNCPROXY_ALLOWED_HOSTS: ${VNCPROXY_ALLOWED_HOSTS:-localhost,127.0.0.1}
      NO_PROXY: ${NO_PROXY:-localhost,127.0.0.1}
      no_proxy: ${NO_PROXY:-localhost,127.0.0.1}
      SELTOOL_COOKIECLOUD_SYNC_CRON: ${SELTOOL_COOKIECLOUD_SYNC_CRON:-0 0 4 * * *}
    volumes:
      - ./config:/app/config
      - ./data:/app/data
    ports:
      - "${HOST_HTTP_PORT:-8081}:8080"
      - "127.0.0.1:${HOST_VNC_PORT:-7900}:7900"
```

****

```ini
IMAGE=ghcr.io/a454735639-lab/seltool-go:latest
TZ=Asia/Shanghai

HOST_HTTP_PORT=8081
HOST_VNC_PORT=7900

# 必改
SE_VNC_PASSWORD=change-me

# 反代或跨机访问 noVNC 时，把域名/IP 加进来
VNCPROXY_ALLOWED_HOSTS=localhost,127.0.0.1

# Docker daemon 若注入了 HTTP_PROXY，需要排除内网
NO_PROXY=localhost,127.0.0.1,192.168.0.0/16,172.16.0.0/12,10.0.0.0/8

# CookieCloud 每日同步时间（唯一没有面板入口的项）
SELTOOL_COOKIECLOUD_SYNC_CRON=0 0 4 * * *
```

> 端口变量**不要**命名成 `VNC_PORT` / `HTTP_PORT`：容器内 entrypoint 用这两个名字
> 控制 x11vnc 和 web 监听，注入会把内部端口改乱（表现为 noVNC 打不开）。

## 配置

分两层，**别混着放**：

### 容器级 → `.env`

| 变量 | 说明 |
|---|---|
| `IMAGE` | 镜像地址 |
| `TZ` | 时区，默认 `Asia/Shanghai` |
| `HOST_HTTP_PORT` | 面板端口，默认 8081 |
| `HOST_VNC_PORT` | noVNC 端口，默认 7900（只绑 127.0.0.1） |
| `SE_VNC_PASSWORD` | VNC 密码，**必改** |
| `VNCPROXY_ALLOWED_HOSTS` | noVNC 允许的 Host，反代时加域名 |
| `NO_PROXY` | 代理排除列表 |
| `SELTOOL_COOKIECLOUD_SYNC_CRON` | CookieCloud 每日同步时间，默认 `0 0 4 * * *` |

> 端口变量不能叫 `VNC_PORT` / `HTTP_PORT` —— 容器内 entrypoint 用这两个名字
> 控制 x11vnc 和 web 监听，注入会把内部端口改乱。

### 应用级 → Web 面板

这些在面板里设置，存到 `config/*.json`，立即生效不用重启：

- **CookieCloud** — 地址 / KEY / 密码、自动导入开关
- **通知** — 企业微信群机器人或应用消息、只在失败时推送
- **MoviePilot** — 地址 / API Token、每天 05:00 自动同步
- **定时任务** — 执行时间、时区
- **站点名表 / 图标** — 可手动覆盖

## 使用

### 添加目标

三种方式：

1. **从 CookieCloud 自动识别** — 面板「导入站点」，按内置域名表 + `pt` 标签挑出 PT 站
2. **手动粘贴** — 每行一个域名或 URL
3. **从模板新增** — 内置常见站模板，自动带好预热路径

导入时重复项默认跳过，勾选「重复时覆盖名称」可批量更新显示名。

### 状态含义

| 提示 | 含义 | 怎么处理 |
|---|---|---|
| 登录过期（Cookie） | CookieCloud 里的 Cookie 失效 | 到 CookieCloud 重新同步 |
| 登录过期（Token） | 站点用 localStorage 凭据 | 到站点重新抓取凭据 |
| 人机验证拦截 | Cloudflare 等防护 | 等它过去，或换出口 IP |
| 域名解析失败 | 域名已不存在 | 找新域名，或删掉目标 |
| 站点拒绝连接 | 连不上 | 站点可能挂了或被墙 |
| 站点故障（5xx） | 服务器报错 | 等站点自己恢复 |
| 页面打不开 | 白屏或超时 | 查站点状态 |

### 通知

失败时推送，按原因分组，只列失败站点。全部成功不打扰（可在面板关掉这个行为）。

## HTTP 接口

登录后带 Cookie 调用，写操作需要 `X-CSRF-TOKEN`（从 `/api/auth/status` 取）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/auth/status` | 登录状态 + CSRF token |
| POST | `/api/auth/login` | 登录 |
| GET | `/api/automation/targets` | 目标列表 |
| PUT | `/api/automation/targets` | 保存目标 |
| POST | `/api/automation/run` | 立即执行 |
| GET | `/api/automation/targets/history/latest` | 各目标最近状态 |
| POST | `/api/automation/targets/retry?url=` | 单站重试 |
| POST | `/api/automation/targets/retry-failed` | 重试全部失败 |
| GET | `/api/automation/targets/candidates` | CookieCloud 里识别到的 PT 站 |
| POST | `/api/automation/targets/import` | 导入目标 |
| GET/PUT | `/api/settings/schedule` | 定时任务 |
| GET/PUT | `/api/settings/cookiecloud` | CookieCloud |
| GET/PUT | `/api/settings/notify` | 通知 |
| GET/PUT | `/api/settings/moviepilot` | MoviePilot 同步 |
| GET | `/api/settings/selfcheck` | 自检：目标数 / 图标覆盖 / 内存 / 定时任务 |

## 构建

### GitHub Actions（推荐）

推 `master` 自动触发：

```
golang 交叉编译 → debian:bookworm-slim 装 chromium/Xvfb/x11vnc → 多架构清单推 ghcr → 导出离线包挂 Release
```

两个架构一次出，产物见 Releases。

### 本地构建

```sh
sh scripts/build.sh            # 校验 → 编译 → 构建 → 部署
sh scripts/build.sh --no-deploy
```

依赖本地的 `seltool-go:runtime-slim2` 基础镜像（比完整 Selenium 镜像小 300MB），
丢了用 `sh scripts/build.sh --base` 重建。

## 数据与备份

| 路径 | 内容 |
|---|---|
| `config/targets.json` | 目标列表 |
| `config/*.json` | 各类设置（含密钥，注意权限） |
| `config/user-account.json` | 管理员账号哈希 |
| `data/chrome-profile` | 浏览器 profile |

备份整个 `config/` 即可，`data/` 可丢弃。

## 已知限制

- 单实例运行（浏览器 profile 有文件锁）
- 目标数上限 500
- 部分站点有 Cloudflare 盾，需要好出口 IP
- 内存占用随任务增长，任务结束后自动释放页缓存

## 致谢

界面外观与功能设计源自 [linzaixian888/selenium-tool](https://github.com/linzaixian888/selenium-tool)（Java 版）。
本仓库是独立重写实现（Go + chromedp），未使用其源代码。

通知模板参考了 [MoviePilot](https://github.com/jxxghp/MoviePilot) 的企业微信双模式设计。

## 许可证

见 [LICENSE](LICENSE)。

**关于上游**：原项目未声明开源许可证，因此本仓库的许可证**不覆盖**其界面设计与命名。
如果你打算以本仓库为基础对外分发，建议先取得原作者许可，或更换全部视觉资产与名称。
