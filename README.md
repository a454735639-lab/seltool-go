# seltool-go

PT 站点自动登录保活工具的 **Go 重写版**。原项目 [linzaixian888/selenium-tool](https://github.com/linzaixian888/selenium-tool) 是 Java 17 + Spring Boot + Selenium；本仓库用 Go + chromedp 重写，保留全部用户可见功能，并把运行镜像从 2.34 GB 压到 683 MB。

> 用途：定期用浏览器打开一批站点，注入 CookieCloud 同步来的 Cookie，检查登录态是否仍然有效。失效时通过企业微信通知你重新登录。

---

## 特性

**核心**

- **逐目标独立标签页** — 单个站点崩溃不影响整轮任务
- **CookieCloud 集成** — 支持外部服务，Web 面板可热更新配置，无需重启
- **CookieCloud 诊断不落缓存** — 503 / 错误密码 / 超时 / 坏 JSON 一律如实报错，不会用旧缓存伪装成功
- **企业微信通知** — 群机器人（markdown）与自建应用（纯文本）双模式
- **失败原因归类** — 自动识别 Cookie 缺失 / 登录态失效 / 站点故障 5xx / 证书过期 / Cloudflare 拦截 / 域名解析失败等 10 类
- **连续失败计数** — 从历史记录计算，附带上次成功时间
- **只在失败时通知** — 全部成功时不推送，可选开关

**Web 控制台**

- 侧边栏布局：桌面固定侧栏，手机汉堡抽屉
- 卡片化目标列表：桌面双列，手机单列
- 深浅色自动跟随系统（`prefers-color-scheme`），也可手动锁定
- 6 种视觉风格 × 8 种配色主题
- 目标导入：从 CookieCloud 自动识别 PT 站，或手动粘贴域名
- 远程浏览器：X11 + x11vnc + noVNC，可在网页里看浏览器实际操作过程

**部署**

- 单容器，无需外部数据库
- 镜像 683 MB（chromium 本体 269 MB 是硬下限）
- 自写 Go 版 WebSocket 代理替代 `websockify`，省掉整个 Python 运行时

---

## 快速开始

### 前置条件

- Docker（支持 amd64 / arm64）
- 一个可用的 [CookieCloud](https://github.com/easychen/CookieCloud) 服务（可选，但推荐）

### 用 compose 启动

`docker-compose.yml`：

```yaml
services:
  seltool:
    image: seltool-go:slim
    container_name: seltool-go
    restart: unless-stopped
    ports:
      - "8081:8080"   # Web 控制台
      - "7900:7900"   # noVNC 远程浏览器
    environment:
      TZ: Asia/Shanghai
      SE_VNC_PASSWORD: change-me       # VNC 密码，务必修改
      # 若 Docker 宿主机配置了 HTTP 代理，需排除容器名与内网段
      NO_PROXY: "localhost,127.0.0.1,172.16.0.0/12,10.0.0.0/8,192.168.0.0/16"
      no_proxy: "localhost,127.0.0.1,172.16.0.0/12,10.0.0.0/8,192.168.0.0/16"
    volumes:
      - ./config:/app/config
      - ./data:/app/data
```

```bash
docker compose up -d
```

打开 `http://<你的地址>:8081`，首次访问会让你注册管理员账号。

### 本地构建

```bash
# 需要 Go 1.25+
CGO_ENABLED=0 go build -o seltool .
CGO_ENABLED=0 go build -o vncproxy ./cmd/vncproxy

# 运行（需要系统已装 chromium）
./seltool -config ./config/custom.yml
```

---

## 配置

配置优先级：**Web 面板保存的值 > `config/custom.yml` > 内置默认值**。环境变量可覆盖 yaml。

### `config/custom.yml`

```yaml
automation:
  listen: ":8080"
  run-on-startup: true          # 启动后立刻跑一轮
  schedule:
    cron: "0 0 8 * * *"          # 六段式：秒 分 时 日 月 周
    zone: "Asia/Shanghai"
  browser:
    headless: false             # VNC 观察用，建议 false
    no-sandbox: true
    disable-dev-shm-usage: true
    page-stay-seconds: 3        # 每站停留时间
    page-load-timeout: 2m
    disable-images: true        # 省流量、加快加载
    user-data-dir: "./data/chrome-profile"
    arguments:
      - "--window-size=1400,900"
      - "--disable-gpu"
  cookie-cloud:
    url: ""                     # 留空则禁用 CookieCloud
    key: ""
    password: ""
  notify:
    only-on-failure: false      # true = 全部成功时不通知
    wecom:
      mode: bot                 # bot = 群机器人；app = 自建应用
      webhook-url: ""
      msgtype: markdown         # markdown 或 text
      # 下面是 app 模式才需要的
      corpid: ""
      corpsecret: ""
      agentid: 0
      touser: "@all"
      proxy: ""                 # API 代理，默认 https://qyapi.weixin.qq.com
```

### 环境变量

| 变量 | 说明 |
|---|---|
| `SELTOOL_WECOM_WEBHOOK_URL` | 企业微信群机器人地址 |
| `SELTOOL_WECOM_MODE` | `bot` / `app` |
| `SELTOOL_WECOM_CORPID` | 企业 ID |
| `SELTOOL_WECOM_CORPSECRET` | 应用 Secret |
| `SELTOOL_WECOM_TOUSER` | 接收人 |
| `SELTOOL_WECOM_PROXY` | API 代理地址 |
| `SE_VNC_PASSWORD` | VNC 连接密码 |
| `TZ` | 时区 |

### 敏感数据存放位置

以下文件由程序在运行时生成，**不要提交到版本库**（`.gitignore` 已排除）：

| 文件 | 内容 |
|---|---|
| `config/user-account.json` | 管理员账号密码哈希 |
| `config/cookiecloud-settings.json` | CookieCloud 地址 / KEY / 密码 |
| `config/notify-settings.json` | 企业微信 corpid / corpsecret |
| `config/cookiecloud-cache.json` | Cookie 缓存（含全部站点凭据） |
| `config/targets.json` | 目标站点列表 |
| `config/target-run-history.json` | 运行历史 |

---

## 使用

### 添加目标

三种方式：

1. **手动添加** — 填 URL 和名称
2. **自动导入** — 从 CookieCloud 已同步的域名里挑出 PT 站（内置域名表 + 域名含 `pt` 标签）
3. **手动粘贴** — 每行一个域名或完整 URL

### 目标状态含义

卡片上的状态标签直接告诉你问题出在哪：

| 标签 | 含义 | 你要做什么 |
|---|---|---|
| 最近成功 | 登录态正常 | 无需处理 |
| Cookie 缺失 / 需重新登录 | CookieCloud 里没有这个站的 Cookie | 浏览器重新登录该站，让扩展同步 |
| 预热地址失败 | 站点 favicon 都打不开 | 站点挂了或域名失效 |
| 站点故障 502 / 503 / 504 | 站点自身故障 | 等站点恢复 |
| 证书问题 | TLS 证书过期或无效 | 站点侧问题 |
| 被站点拦截 | Cloudflare 挑战页 | 换 IP 或等挑战解除 |
| 浏览器异常 | 浏览器启动/导航失败 | 看容器日志 |

### 通知样式

任务结束后推送（markdown 模式）：

```
**selenium-tool · 定时执行完成（存在失败）**
> 10-19 15:33 · 46 目标 · 成功 34 · 失败 12 · 用时 3 分 12 秒

> **失败 12**
> [pt.keepfrds.co](https://pt.keepfrds.co/) · 预热地址失败 · 连续 3 次（上次成功 10-14 09:00）
> [hddolby.com](https://www.hddolby.com/) · 被站点拦截 · 首次失败

> **成功 34**：ubits.club、hdtime.org、ourbits.club、pterclub.net、hdfans.org …

> 需处理：springsunday.net、novahd.top 需要重新登录并同步 Cookie
```

---

## HTTP 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/auth/status` | 登录状态 + CSRF token |
| `POST` | `/api/auth/login` | 登录 |
| `POST` | `/api/auth/register` | 注册（仅首次可用） |
| `POST` | `/api/auth/logout` | 登出 |
| `GET` | `/api/automation/targets` | 目标列表 |
| `PUT` | `/api/automation/targets` | 保存目标 |
| `POST` | `/api/automation/targets/import` | 导入站点 |
| `POST` | `/api/automation/targets/retry` | 重试单个目标 |
| `POST` | `/api/automation/targets/retry-failed` | 重试全部失败目标 |
| `GET` | `/api/automation/targets/history` | 某目标最近 7 次记录 |
| `GET` | `/api/automation/targets/history/latest` | 全部目标最新状态 |
| `POST` | `/api/automation/run` | 立即执行一轮 |
| `GET` | `/api/automation/status` | 任务是否在运行 |
| `GET` / `PUT` | `/api/settings/cookiecloud` | CookieCloud 配置 |
| `POST` | `/api/settings/cookiecloud/test` | 测试连接（不写缓存） |
| `GET` / `PUT` | `/api/settings/notify` | 通知配置 |
| `POST` | `/api/settings/notify/test` | 发送测试消息 |

除 `GET` 与登录接口外，所有写请求需带 `X-CSRF-TOKEN` 头。

---

## 构建镜像

```bash
# 1. 编译（建议在 golang:1.25-alpine 容器内，避免 glibc/musl 差异）
docker run --rm -v "$PWD:/src" -w /src golang:1.25-alpine \
  sh -ec 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/seltool . &&
          CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/vncproxy ./cmd/vncproxy'

# 2. 打包镜像
docker build -f deploy/Dockerfile.slim -t seltool-go:slim .
```

### 镜像瘦身要点

从 969 MB 降到 683 MB，砍掉的都是 **chromium 只声明 `Recommends`（非硬依赖）** 的大包：

| 移除 | 省下 | 原因 |
|---|---|---|
| `libllvm15` + `libz3-4` | 128 MB | chromium 软件渲染走 SwiftShader，不用 LLVM 后端 |
| `libgl1-mesa-dri` | 23 MB | 同上，Xvfb 只需 `libGL.so.1` 存根 |
| `libgs10` + `poppler-data` + `fonts-urw-base35` | 55 MB | ghostscript 链，由 fluxbox→imlib2→spectre 间接拉入 |
| `adwaita-icon-theme` | 21 MB | GTK 图标主题，无窗口装饰也用不上 |
| `systemd` 系 | 19 MB | 容器内不需要 init |

**两个坑**：

1. **别删 `libgl1` / `libimlib2`** — Xvfb 硬依赖 `libGL.so.1`，fluxbox 硬依赖 `libImlib2.so.1`，删了组件直接起不来。
2. **`docker commit` 不减小镜像** — 它只记录「删除标记」，体积反而涨。必须 `docker export` → `docker import` 压平才能得到真实体积。

---

## 开发

```bash
go test -short ./...        # 单元测试（10 个测试文件）
go test ./...               # 含需要 chromium 的集成测试
```

项目结构：

```
├── main.go              程序入口、配置加载、定时任务注册
├── server.go            HTTP 路由、认证、CSRF、各设置接口
├── automation.go        任务编排、通知触发
├── browser.go           chromedp 浏览器控制、Cookie 注入
├── cookiecloud.go       CookieCloud 拉取、域名过滤、诊断
├── wecom.go             企业微信双模式发送
├── notify_format.go     通知模板：失败归类、连续计数、颜色标记
├── targets.go           目标存储与模板校验
├── store.go             历史记录、告警状态
├── web/                 前端（go:embed 内嵌进二进制）
└── cmd/vncproxy/        Go 版 WebSocket 代理，替代 websockify
```

**注意**：前端是 `go:embed` 内嵌的。改 `web/index.html` 后**必须重新 `go build`**，否则改动不生效。

---

## 已知限制

- 站点失败多由**数据/网络侧**导致（Cookie 过期、域名失效、证书过期、Cloudflare 挑战），不是程序缺陷。程序会如实归类并提示你处理。
- 单个目标失败**不会**自动重试，避免触发站点风控；需要手动点「失败重试」。
- 历史记录只保留每个目标最近 7 次。

---

## 致谢

界面外观与功能设计源自 [linzaixian888/selenium-tool](https://github.com/linzaixian888/selenium-tool)（Java 版）。本仓库是独立重写实现，未使用其源代码。

通知模板参考了 [MoviePilot](https://github.com/jxxghp/MoviePilot) 的企业微信双模式设计。

---

## 许可证

见 [LICENSE](LICENSE)。

**关于上游**：原项目未声明开源许可证，因此本仓库的许可证**不覆盖**其界面设计与命名。如果你打算以本仓库为基础对外分发，建议先取得原作者许可，或更换全部视觉资产与名称。
