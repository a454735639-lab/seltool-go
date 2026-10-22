# Changelog

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
