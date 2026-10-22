# Changelog

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
