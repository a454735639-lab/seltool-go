# F1 实施进度

## 2026-10-18 功能阶段
- 用户授权开始执行已确认方案。
- 工作副本 `/var/minis/workspace/seltool-f1`；生产源目录保持不变。
- 已固定生产镜像 `00e0b6ea1e2a…c836` 并创建 `seltool-go:rollback-20261018` 标签。
- 并行范围：CookieCloud诊断；模板/YAML/导入；VNC代理/启动等待。主线程负责浏览器资源、Cookie语义、启动通知和集成验收。
- 阶段门禁：先F1验收，再构建瘦身候选；未全通过不发布。
- 决策：生产上的旧目标/历史不做自动修正，不触发批量PT签到；验证使用mock站点。

## 2026-10-19 UI 重构阶段（已发布）
生产镜像：`sha256:535a1516f770…64da4`（candidate-safe 标签同步）
回滚镜像：`seltool-go:rollback-20261018` = `00e0b6ea1e2a…c836`（948MB，改版前）

### 已交付
1. **通知模板重做**（`notify_format.go`）— 失败原因归类（10 类）、连续失败次数、单行压缩、成功列表折叠、耗时、颜色标记（markdown 用 `<font>`，纯文本剥离）、末尾「需处理」行动项。新增 `only-on-failure` 开关（面板可配）。
2. **响应式修复** — 375px 横向溢出 52px 消除；底部操作栏手机端由 `static` 改 `fixed` 常驻；导入入口移入侧边栏。
3. **侧边栏布局** — 桌面 236px 固定侧栏 + 手机汉堡抽屉；顶栏 8 按钮与底栏 5 按钮分层归位。
4. **Agent2API 主题** — 从 `agent2api:3065` 的 `ui.css` 提取 `--ui-*` 设计令牌（oklch→hex），新增配色主题 `agent`，`prefers-color-scheme` 自动跟随系统 + 手动锁定。
5. **卡片排版 v2** — 去重复域名、序号左移、失败归类上卡面、最近运行时间、独立下拉箭头；桌面双列（页面高 4767→2934px）；字号 15→13 种。

### 关键坑（勿重犯）
- CSS 变量名：应用用 `--text`/`--page`，非 `--ink`/`--bg`。未定义变量的 `var()` 声明在 computed-value 阶段失效→透明，`!important` 无效。改前先 `rg '(--[a-z0-9-]+)\s*:'` 列全量。
- 前端走 `go:embed`：改 `web/index.html` 必须 `go build` 再 `docker build`，否则镜像 ID 不变、改动不生效。
- `/` 无 Cache-Control/ETag，浏览器吃旧页面；验证加 `?v=N`，用户端需强刷。
- 本机 WebView 中 CSS transition 不推进，`getComputedStyle` 读到过渡起始值→误判样式未生效。临时注入 `transition:none!important` 即可辨真伪。
- 主题规则带 `html[data-visual-style=…]` 前缀（特异性 (0,2,1)），覆盖需 `.shell .side X` / `.shell .main X`（(0,3,0) 起）。

### 遗留
- 字号未彻底收敛到 6 档（余 7 档分布在弹窗/历史/模板选择器等次级界面，改需逐屏核对）。
- 11–13 个 PT 站持续失败，均为数据/网络侧（需重登、域名失效、证书过期、Cloudflare），非代码缺陷。
- 企业微信通知已切 `app` 模式 + `onlyOnFailure=true`；markdown 模板尚未在真实群机器人上验证。
- `/volume1/docker/seltool-f1-work`（238MB）保留，含验收证据。
