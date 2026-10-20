# 动态验收用例明细

| 用例 | 结果 | 证据 |
|---|---|---|
| 首次启动无用户 | PASS |  |
| 未登录API拒绝 | PASS |  |
| 匿名CSRF拒绝 | PASS |  |
| 注册管理员 | PASS |  |
| 重复注册拒绝 | PASS |  |
| 错误密码拒绝 | PASS |  |
| 正确密码登录 | PASS |  |
| 登录态与用户名 | PASS |  |
| 写操作CSRF拒绝 | PASS |  |
| 首页和index.html | PASS |  |
| 静态认证资源 | PASS |  |
| 内置4个模板 | PASS |  |
| 非法URL拒绝 | PASS |  |
| 目标保存读取 | PASS |  |
| 凭据不回显密码 | PASS |  |
| CookieCloud测试计数 | PASS |  |
| 留空沿用密码 | PASS |  |
| 手动执行202 | PASS |  |
| Cookie与localStorage均有效 | PASS | 执行成功 |
| HTTPOnly Cookie实际送达 | PASS |  |
| 目标历史可读取 | PASS |  |
| 运行互斥409 | PASS |  |
| 单站失败不中断下站 | PASS |  |
| 不存在目标重试404 | PASS |  |
| 失败批量重试仅选失败 | PASS |  |
| 单目标重试 | PASS |  |
| 历史保留7条 | PASS |  |
| 手动导入保留路径去重 | PASS |  |
| 重复导入不新增 | PASS |  |
| 测试连接不应掩盖远端503 | FAIL | HTTP 200; ok=True |
| 远端失败时缓存回退 | PASS |  |
| 测试连接应拒绝错误密码 | FAIL | HTTP 200; ok=True |
| Webhook收到任务汇总 | PASS |  |
| 登出成功 | PASS |  |
| 登出后API拒绝 | PASS |  |
| 重启持久化 user-account.json | PASS |  |
| 重启持久化 targets.json | PASS |  |
| 重启持久化 cookiecloud-settings.json | PASS |  |
| 重启后仍已注册 | PASS |  |
| 重启启动任务真实执行 | PASS |  |
| 六段Cron定时真实执行 | PASS |  |
| 重启后浏览器仍成功 | PASS | 执行成功 |
| 原版YAML目标配置生效 | FAIL | 实际目标数=0 |
| 拒绝不存在模板ID | FAIL | HTTP 200 |
| 模板固定字段不可篡改 | FAIL | HTTP 200 |
| 模板必填localStorage不可为空 | FAIL | HTTP 200 |
