# Changelog

## 1.0.9 (2026-10-23)

### 新增

- **页面角落显示镜像版本号**：数据来自新接口 `GET /api/version`（公开、只返回版本字符串）。
  - 首页：头部右上角徽标
  - 登录页 / 注册页：左下角
  - 版本号编译期通过 `go:embed VERSION` 嵌入二进制，也可用 `-ldflags "-X main.buildVersion=x.y.z"` 覆盖。

### 移除

- **删除 H5 快捷指令页整套功能**（只走企业微信指令，不用网页）：
  - 删除页面 `web/pt-panel.html` 与路由 `GET /pt-panel`
  - 删除接口 `POST /api/pt-panel/token`、`GET /api/pt-panel/summary`、
    `POST /api/pt-panel/retry-failed`、`POST /api/pt-panel/retry`
    （这 4 个接口原本不走登录态、仅靠 URL 里的 `?key=` 换 2 小时令牌）
  - 删除通知设置里的「快捷指令页密钥」「外网域名」两项，及 `panelKey`/`panelHost` 配置字段
  - 删除 `panel.go`、`panelTokenStore`、`panelLimit`、`genPanelKey()`
  - 「一键创建企微菜单」保留并移到「交互式命令」区域，改名为「一键创建企微指令菜单」：
    菜单项全是 click 类型（点了发一条文字指令），本来就不需要域名和密钥

### 修复

- **微信指令「同步 cookie」不新增站点**：原来这条指令只刷新缓存、报告覆盖情况，
  即使开着「自动导入新站点」也不会把 CookieCloud 里的新 PT 站加进目标列表
  （面板「同步」和每日同步都会加，只有指令这条路径漏了）。
  现已与另外两条路径一致：自动导入开启时同步会顺带导入，并在回复里列出新增站点。

### 优化

- **通知设置整理**：`交互式命令回调`（回调 Token / EncodingAESKey / 允许的微信用户）
  与「通知」本身无关，收进默认折叠的「高级：微信内交互命令」区域。
  通知设置区只保留：推送方式、Webhook、消息格式、企业微信应用凭据、API 代理、只在有失败时通知。
  字段、接口、保存逻辑均未改动，只是收纳位置变化。

## 1.0.8 (2026-10-23)

### 清理

纯仓库清理，**运行时行为与 1.0.6 完全一致**，不含功能改动。

- 删除一次性调试脚本：`audit-image.sh`、`bisect-vnc.sh`、`preview.sh`、`test-reclaimable.sh`、
  `test-reclaimable2.sh`、`verify_schedule.py`、`progress.md`。
- 删除 `acceptance/`（26 个一次性验收脚本，自带独立 `go.mod` 模块 `seltool-acceptance-tools`，
  与主模块无引用关系）。
- 删除 `.gitea/workflows/build-image.yml`（Gitea 时代的旧 CI，仓库迁移到 GitHub 后已分叉且不再触发）。
- 删除 `docs/superpowers/plans/`（2026-10-18 的规划文档）。
- 删除已失效的本地构建链：`scripts/build.sh`（写死 `image/deploy/Dockerfile.f1`，而仓库中并无 `image/` 目录，
  实际依赖 NAS 上的 `/volume1/docker/seltool-f1-work` 工作副本）、`scripts/deploy-and-backup.sh`、
  `scripts/check-frontend.py`、`scripts/backup-obsidian.sh`、`make-runtime-slim.sh`、`make-runtime-slim2.sh`、
  `deploy/Dockerfile.slim`、`deploy/Dockerfile.ci`、`deploy/Dockerfile.f1`、`deploy/Dockerfile.safe`。
  镜像构建统一走 CI 的 `deploy/Dockerfile.multiarch`（README 的「本地构建」章节已同步更新）。
- 删除 3 处无人调用的 Go 函数：`browser.go` 的 `buildWarmupURL()`（实际调用点是 `buildWarmupURLFor()`）、
  `notify_format.go` 的 `mdLinkText()`、`targets.go` 的 `(*TargetStore).templateStore()`。

### 说明

- 1.0.7（按目标选择网络出口）已回退，代码保留在分支 `v1.0.7-wip`，未合并。
  回退原因：全局默认改直连后，GTK、PT时间 两站失败（1.0.6 时它们走默认 profile，带有历史 `cf_clearance`；
  切换出口时 profile 一并更换，导致 clearance 丢失）。
- 保留 `cmd/dropcache`、`cmd/httpprobe`（容器内排障用的小工具，不参与镜像构建）。

## 1.0.6 (2026-10-23)

### 新增

- **目标级「直连」开关（`noProxy`）**。应用的浏览器代理一直是全局的（`notify-settings.json` 的 `proxy`），
  没有按目标绕过的能力。43 站复核时实测：蟹黄堡（crabpt.vip）、青蛙（qingwapt.com）在**直连**的 Chromium
  下能正常打开登录页，走代理则整页打不开，于是被判成「403 / 等待页面加载超时」——但用同一批 Cookie 直连
  抓首页是能看到 `logout.php` 的，账号本身是登录状态。现在可以给单个目标开直连。

### 实现

- `config.Target` 新增 `noProxy`（yaml / json 同名，默认 `false`，旧配置无需迁移）。
- 浏览器实例改为**按模式缓存**（`proxy` / `direct`）：直连模式不注入全局代理，并使用独立的 profile 目录
  `<userDataDir>-direct`，避免 Chromium 的 profile 单实例锁互相阻塞；批量执行时每个目标各取所需，
  只有首次切换到某模式时才新建实例。
- 面板「登录判定（可选）」折叠区新增「直连」勾选框，保存即生效。

### 注意

- 直连意味着该站看到的是你本地出口 IP，与代理出口不同。PT 站若对 IP 变化敏感，请确认后再开。
- 只影响浏览器访问，CookieCloud 同步与站点表请求不受影响。

## 1.0.5 (2026-10-23)

本轮全部来自真实排障：一处「代码支持但界面没有」，一处「数字对不上」，一处「上个版本没清干净」。

### 新增

- **面板可直接配登录判定**：目标卡片新增「登录判定（可选）」折叠区，可编辑 `loginPath` /
  `okSelector` / `failSelector`。此前这三项在 `config.Target` 里是有的（`config.go:132-134`），
  但面板没有输入框 —— 想让某个站的登录判定真正生效，只能手改 `config/targets.json` 再重启。
  保存时沿用 `{...原对象, ...表单}` 的合并方式，模板锁定的目标仍不可改。

### 修复

- **快捷页汇总只统计当前目标**：`handlePanelSummary` 原来遍历全量历史，已删除的目标会永久挂在
  失败清单里，且 `success + failed` 会大于 `total`（线上实测 36 + 8 = 44，而 `total` 是 43）。
  现在先与当前目标取交集，并新增 `notRun` 字段，使 `total = success + failed + notRun` 自洽。
- **`make-runtime-slim*.sh` 不再烘焙 `SE_VNC_PASSWORD=selenium`**：1.0.4 已从 Dockerfile 与
  entrypoint 里拿掉这个默认口令（改为启动时随机生成），但这两个本地辅助脚本仍把 `selenium`
  写进镜像 `ENV`。口径对齐，避免用它们产出的镜像又带回默认口令。

### 文档

- README 新增两节：**登录判定**（三个字段的语义、默认规则为何可能没有区分度、怎么用「填无效
  localStorage」来验证判定是否可信）与 **站点换域名 / 一个站多个域名**（三层域名匹配规则、
  `cookieDomain` 的用途、换主域的四步清单、旧域名失效为何不会自动消失）。

## 1.0.4 (2026-10-23)

交付质量批：让「构建出来的东西」和「声称的东西」一致，并把快捷页的密钥从 URL 里挪走。

### 交付 / CI

- **默认镜像名修正**：`deploy/docker-compose.yml` 原来默认 `ghcr.io/a454735639-lab/seltool-go`，
  而 CI 实际推的是 `seltoolgo`（无连字符）—— 那个包是删库留下的孤儿，跟着模板走的人会拉到
  一个永不更新的旧镜像。现在默认 `asice99/seltool-go:latest`。
- **CI 改成「先验证后推送」**：以前是构建 → 直接推 Docker Hub/GHCR → 再冒烟，冒烟挂了
  坏镜像已经出去了。现在顺序是 `gofmt/vet/test` → 构建 amd64 本地镜像 → 冒烟（含
  `/healthz`、`/login.html`、`?k=` 必须被拒、noVNC 端口）→ 全过了才登录并推送多架构。
- **`VERSION` 读取失败直接报错**：原来是 `cat VERSION || echo 0.0.0`，文件丢了会静默产出
  `0.0.0` 镜像和 `build-...` 标签，事后极难追溯。现在缺失或不是 `x.y.z` 就 fail。
- **Release tag 改成 `v$VERSION`**（原来 `build-YYYYMMDD-sha`），版本号能直接从 tag 看出来。
- **新增 `go-check` 工作流**：任意分支/PR 都跑 `gofmt -l`（非 vendor）+ `go vet` + `go test`，
  不用等镜像构建就能知道代码有没有问题。
- **`.dockerignore` 排除 `.env`**：Dockerfile 有 `COPY . .`，之前 `.env`（含
  `SE_VNC_PASSWORD`）会被打进构建层，`docker history` / 解包镜像都能扒出来。
- **compose 补齐运维参数**：`logging` 轮转（20m × 5，此前 json-file 无限增长）、
  `shm_size: 1gb`（Chromium 默认 64MB `/dev/shm`，多标签页必崩）、`healthcheck` 打
  `/healthz`、`security_opt: no-new-privileges`、`stop_grace_period: 30s`。
  **没加 `cap_drop: ALL`**：容器以 root 跑 Chromium 且带 `--no-sandbox`，丢光能力会影响
  文件属主改写，需要单独验证后再上。

### 安全

- **noVNC 默认口令 `selenium` 移除**（`entrypoint.sh`、`Dockerfile.slim`、`Dockerfile.safe`）：
  未配置 `SE_VNC_PASSWORD` 时随机生成 16 位并在启动日志打印一次。默认口令 + 端口暴露
  = 任何人都能接管浏览器里的已登录会话。
- **快捷页密钥不再随每次请求出现**：`?k=<面板密钥>` 以前直接拼在每个数据接口上，
  会进反代 access_log、浏览器历史、企微菜单截图。现在：
  1. 页面带 `?k=` 打开，只用来 `POST /api/pt-panel/token` 换一枚 **2 小时**令牌；
  2. 换完立刻 `history.replaceState` 把 `k` 从地址栏抹掉；
  3. 之后所有 `/api/pt-panel/*` 只认 `X-Panel-Token` 请求头，`?k=` 一律 403。
  老菜单链接照常可用（页面自己完成兑换）。
- **密钥兑换接口带失败限速**：5 次/10 分钟锁 15 分钟，避免拿 18 位密钥去撞库。
- **设置页新增「随机生成」**：一键填 32 位随机密钥（`crypto.getRandomValues`），
  并显示为明文方便复制；重新生成后点「一键创建企微菜单」即可换掉旧链接。

### 新增接口

- `GET /healthz` → `{"ok":true}`，无鉴权、不读数据，供容器健康检查使用。

## 1.0.3 (2026-10-23)

本轮来自一次全量安全 + 健壮性审计（报告见 `shared/seltool-go-audit/`），修的是
「会静默骗人」和「能被外人利用」两类问题。

### 安全

- **存储型 XSS（高危）**：`looksLikeImage` 以前只校验 `data:` 逗号后的 base64，
  不校验 MIME 前缀。MoviePilot 返回 `data:x"><img onerror=...>;base64,<合法PNG>`
  即可注入；前端 `iconTag()` 又直接拼 `innerHTML`。现在 MIME 走白名单
  （png/jpeg/gif/webp/ico，**不含 svg**）+ 单条 4MiB 上限，前端改用 `esc()` 转义。
  被注入时可同源读走 panelKey / webhook / CookieCloud KEY，故列为最高优先级。
- **登录零限速 → 已加**：同 IP 或同用户名 10 分钟内失败 5 次即锁定 15 分钟，
  返回 429 + `Retry-After`。此前 bcrypt 再慢也挡不住并发撞库。
- **会话永不过期 → 已加 TTL**：24 小时过期，超过 2048 条按最久未访问淘汰；
  过期项在 `Get` 时删除。此前 `/api/auth/status` 每次匿名请求都新建一条永久会话，
  可被用来把内存撑爆。
- **Cookie 缺 `Secure`**：反代终止 TLS 时 `r.TLS` 恒为 nil，旧判断永远为 false。
  改为「内网/回环来源才采信 `X-Forwarded-Proto`」，公网直连伪造该头无效。
- **企微回调白名单默认放行 → 改为拒绝**：空白名单、含 `*`、`zhang*li` 这类输入
  以前一律放行所有人。现在空白名单 = 全部拒绝，`*` 不再有通配语义（前端文案同步）。
- **凭据原文回显 → 只给「已配置」**：`/api/settings/notify` 的 webhookUrl /
  callbackToken / panelKey 和 `/api/settings/cookiecloud` 的 KEY 不再回显原文，
  改为 `has*` 布尔 + 前端占位提示；留空提交即沿用原值。

### 健壮性

- **CDP 握手无超时 → 会挂死整个进程**：`NewBrowser` 里的 `chromedp.Do` 在 `runMu`
  锁内，握手不返回时 `TaskError()`/`Shutdown()` 全部堵死，只能 kill 容器。
  现在用 goroutine + select 兜底（不能给 browserCtx 套 `WithTimeout`，那会销毁
  CDP 绑定）：启动 60s、建标签页 20s，超时即 `Close()` 让阻塞中的 `Do` 退出。
- **Cookie 注入失败仍记成功 → 假绿**：持久化 profile 里残留旧会话时，注入 0 个
  cookie 也能通过登录态检查，CookieCloud 早已失效却长期显示正常。现在
  「解析到 cookie 但注入 0 个」和「旧 cookie 清不掉」都直接判失败。
- **`normalizeDomain` 不剥 scheme/端口/路径**：`https://hdtime.org` 这类两段域名
  会变成 0 项、静默失败（馒头那次「填 https://kp.m-team.cc」的代码层原因）。
  现在走 `url.Parse` + 兜底裁剪，并补了表驱动测试（14 种写法）。
- **读-改-写丢更新**：面板保存与定时自动导入各自 `Get → merge → Save`，`Save` 是
  整表替换，并发时后写者覆盖前者 —— 表现为「刚加的目标凭空消失」。
  新增 `TargetStore.Update()`，在写锁内完成读-改-写。
- **死浏览器被当站点故障**：登录判定失败未走 `classify()`，`checkStatus` 报
  「浏览器已关闭」也变成普通失败，不触发 `handleBrowserClosed`，剩下几十站
  逐个拿死浏览器跑到超时。现在按 `ErrBrowserClosed` 归类。
- **`runMu` 顺带保护 `lastError`**：浏览器启动慢（数十秒）时面板查状态全阻塞。
  拆出独立的 `errMu`。
- **panic 路径不回收浏览器**：异常轮次约 300MB 的 Chromium 不回，现加 `defer`。
- **重试等待不可取消**：`time.Sleep(60s)` 改为 `sleepOrStop`，`Shutdown` 时立即退出。
- localStorage 多域命中顺序确定化（最具体域优先 + 按 key 去重），不再依赖 map 遍历随机序。

### 测试
- 新增 `normalize_test.go`（14 个域名写法）与 `security_test.go`
  （限速锁定/解锁、白名单拒绝、会话 TTL/上限、图标 MIME 注入、反代 HTTPS 判定）
- `gofmt` / `go vet ./...` / `go build ./...` / `go test ./...` 全部通过

### 未包含（列入 1.0.4）
- 默认镜像名 `ghcr.io/a454735639-lab/seltool-go`（孤儿包）改 `seltoolgo`
- CI 改为「先冒烟后推送」并加 `go test`；`VERSION` 读取失败改为报错而非静默降级
- `.dockerignore` 补 `.env`；compose 补 `logging` 轮转 / `shm_size` / `healthcheck` / `cap_drop`
- 去掉 `entrypoint.sh` 等三处 `selenium` 默认口令
- panelKey 走 Header 而非 URL 查询串，并支持一键生成 32 位随机值

## 1.0.2 (2026-10-22)

### 修复：只存在于 localStorage 的站点无法导入
- **根因**：导入候选（`detectPTSites`）只扫描 `cookie_data` 的域名。
  像馒头（M-Team）这类登录态只写在 localStorage、没有有效 cookie 的站点，
  永远进不了候选列表，也无法自动导入。
- 新增 `detectPTSitesWithStorage()`：同时扫描 `cookie_data` 与 `local_storage_data` 的域名，
  任一来源命中即视为可用站点（`detectPTSites` 保留为兼容包装）
- `RunAutoImport()` 增加 `lsData` 参数；**只要 cookie 或 localStorage 任一非空**即继续，
  不再因 cookie 为空而直接返回
- 全部调用点已更新：手动同步 `/api/cookiecloud/sync`、候选预览 `/api/.../candidates`、
  导入 `/api/.../import`、每日定时同步（`main.go`）、企业微信「同步 Cookie」「导入站点」
- 候选接口新增 `source` 字段（`cookie` / `localStorage` / `both`），便于定位站点靠哪种凭据登录
- 同步结果文案带出 localStorage 统计（有数据时才显示，避免噪音）
- 企业微信「缺 Cookie」改称「缺凭据」，localStorage 也算凭据

### 说明
- 需 CookieCloud 扩展开启「同步 LocalStorage」且服务端 `local_storage_data` 非空才有效果；
  为空时行为与 1.0.1 完全一致

### 测试
- `go build ./...` / `go vet ./...` / `go test ./...` 全部通过

## 1.0.1 (2026-10-22)

### 新增：CookieCloud 同步 localStorage
- 解析 CookieCloud 的 `local_storage_data` 字段，按目标域名自动注入 localStorage
  （如 Rousi Pro 的 `token` 这类只存在 localStorage 里的凭据）
- **优先级**：目标手动配置的 `localStorage` 优先；为空时才使用同步值，不覆盖人工设置
- 兼容两种数据格式：`{domain: {key: value}}` 与 `{domain: [{name, value}]}`
- 新增 `FetchAll()`（一次返回 Cookie + localStorage），`FetchAllCookies()` 保留兼容
- 新增 `LocalStorageFor()`：按域名匹配（支持子域、`www.` 前缀），复用现有 Cookie 域名逻辑

### 说明
- 需要 CookieCloud 浏览器扩展开启「同步 LocalStorage」才会导出该字段
- 未开启时 `local_storage_data` 为空，行为与 1.0.0 完全一致（不注入，不影响现有站点）

### 测试
- 新增 `TestParseLocalStorageData`、`TestParseLocalStorageDataMissing`、`TestLocalStorageForMatchesDomain`

## 1.0.0 (2026-10-22)

首个语义化版本（1.0.0）。此前为无版本号的滚动更新。

### 构建与分发
- CI 双推：ghcr（`ghcr.io/a454735639-lab/seltoolgo`）+ Docker Hub（`asice99/seltool-go`）
- 引入 `VERSION` 文件管理版本号，镜像打 `:<version>` 标签（同时保留 `:latest` / `:sha-<短哈希>` / `:<日期>-<短哈希>`）
- Dockerfile 增加 `apt-get upgrade -y`，升级基础镜像全部软件包，降低已知漏洞

### 本版本包含的功能
- PT 站点保活巡检（chromedp，43 目标，CookieCloud 自动同步）
- 企业微信双向交互：回调命令（STATUS / RUN_ALL / RETRY_FAILED / FAILED_LIST / HELP 等）、点击菜单、H5 快捷页 `/pt-panel`
- 8 套主题（默认毛玻璃）
- 自检接口 `GET /api/settings/selfcheck`
- 任务结束页缓存释放（fadvise），内存回落
- 失败原因通俗化分类（登录过期 / 人机验证 / 证书失效 / 域名解析 / 站点故障等）
- 瞬时失败自动重试（批次结束后 60 秒，上限 10）
