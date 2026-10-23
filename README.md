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

> **`config/` 必须挂载**。容器内的 `/app/config` 是临时的，不挂载的话
> 目标列表、CookieCloud、通知、管理员账号都会随容器重建而丢失。
> `.env` 不会进镜像（避免把密码打包给拉镜像的人），它只在宿主机被
> `docker compose` 读取，用来替换 compose 里的占位符。

打开 `http://<你的IP>:8081`，首次进入注册管理员账号。

noVNC 实时观看（仅本机）：`http://127.0.0.1:7900`

### 配置文件内容

不想下载模板的话，直接照抄这两份。

**docker-compose.yml**

```yaml
services:
  seltool:
    image: asice99/seltool-go:latest
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

**.env**

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

### 登录判定（可选，站点改版时用）

每个目标可展开「登录判定（可选）」配置三项，留空则走默认规则（NexusPHP 标记 → 标题关键字 → 登录表单启发式）：

| 字段 | 作用 | 示例 |
|---|---|---|
| `failSelector` | **仅未登录时**出现的元素，命中即判「登录状态检查失败」 | `.login-form` |
| `okSelector` | **仅登录后**出现的元素，命中即判成功 | `a[href="/logout.php"]` |
| `loginPath` | 未登录时会被重定向到的路径，命中即判失败 | `/login` |

同一套默认规则对结构相近的站点**可能没有区分度**（有凭据、没凭据都报「成功」）。要确认判定是否可信，可临时把目标的 `localStorage` 手动填上无效值 —— 非空即跳过 CookieCloud 注入，等价于「无凭据」场景；判定正确时应报「登录状态检查失败」（用完整置空的凭据可让浏览器真正退回未登录态）。验完记得清空，否则手动配置会一直顶掉同步数据。

### 网络出口：按目标选直连 / 走代理（`proxyMode`）

默认**直连**。每个目标可以单独指定出口：

| `proxyMode` | 含义 |
|---|---|
| `""`（默认） | 跟随全局默认（`browser.proxy-mode`，未配置时=直连） |
| `direct` | 强制直连：加 `--no-proxy-server`，忽略容器里的 `http_proxy` 环境变量 |
| `proxy` | 走 `browser.proxy` 配置的代理 |

- 面板里在目标卡片的「登录判定（可选）」区选「网络出口」即可，保存即生效。
- `proxy` 只有在 `custom.yml` 里配置了 `browser.proxy` 时才生效；没配地址会自动按直连处理，并在日志里提示。
- 两种出口各用一个独立的 profile 目录（直连是 `<userDataDir>-direct`），避免 Chromium 的 profile 单实例锁互相阻塞。
  浏览器实例按出口缓存，批量执行时每个目标各取所需，只有首次切到某模式时才新建实例。
- 旧字段 `noProxy: true` 仍然有效，等价于 `proxyMode: direct`。

> **profile 会积累反爬 Cookie，导致「403 / 维护中」的假故障。**
> `data/chrome-profile*` 里长期跑会堆下 `cf_clearance` 等 Cookie，被 Cloudflare 判定异常后会整站返回 403
> 或「temporary maintenance」。排查时先换一个全新 profile 试同一 URL：
> `docker exec seltool-go rm -rf /app/data/chrome-profile*`（删掉后下次运行自动重建，凭据本来每轮都从 CookieCloud 重新注入）。

### 站点换域名 / 一个站多个域名

**同主域换子域**：无需改配置。匹配时目标域名会逐级剥离后缀（`kp.m-team.cc` → `m-team.cc`），站点识别表也做同样的后缀回退；日志里能看到 `允许匹配域名 [kp.m-team.cc m-team.cc]`。只需保证新域名也在 CookieCloud 同步白名单里。

**换成不同主域**，按顺序做四件事：

1. **补白名单**：新域名加进 CookieCloud 同步白名单再同步一次。采集端按白名单过滤（子串匹配），不在名单里的站连采都不采。
2. **改目标 URL 为凭据实际所在域**：Cookie 的 `Domain` 属性决定浏览器把它发给谁 —— 凭据存在 A 域、目标却访问 B 域，注入了也发不出去，照样登录失败。
3. **不想改 URL 就设 `cookieDomain`**：CookieCloud 匹配优先用它，可让旧目标 URL 去取另一域名下的凭据。面板暂无此项输入框，需改 `config/targets.json`（面板保存不会丢掉这类字段）。
4. **同步站点表**：面板的 MoviePilot 同步（或企微「站点导入」）拉一次，新域名才会被自动识别与导入。

**旧域名失效不会自动消失**：会每轮报「域名解析失败」，站点表里也仍然留着，需要手动改 URL 或停用。

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
