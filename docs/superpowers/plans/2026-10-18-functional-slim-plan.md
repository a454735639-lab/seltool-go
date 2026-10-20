# seltool-go 保功能瘦身 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 先修复功能验收阻塞项并固定通过版本，再在不损失原作者及当前新增功能的前提下缩小运行镜像。

**Architecture:** 保留Go + Chromium + Xvfb + fluxbox + x11vnc + noVNC，继续使用Go WebSocket代理替代Python websockify。保持Debian/glibc作为默认运行基础，先做同版本低风险构建优化，再评估基于实际依赖证据的运行文件裁剪。CookieCloud外部连接与本地可选服务解耦，不为应用瘦身强制迁移用户凭据。

**Tech Stack:** Go >=1.25（现有NAS构建器golang:1.26-alpine）、chromedp v0.21.0、Chromium、Debian bookworm-slim、noVNC v1.6.0、gorilla/websocket、Docker Compose。

**Spec:** `docs/superpowers/plans/2026-10-18-functional-slim-spec.md`

## Global Constraints

- 本文是方案，不授权立即构建/重启生产；用户安排执行。
- 项目本地：`/var/minis/workspace/seltool-go`；NAS：`/volume1/docker/seltool-go`。
- 控制台：`http://10.0.0.3:8081`，登录页与状态接口已实测HTTP200。不强制移除10.0.0.4的现有可达路径。
- NAS的10.0.0.3沿用NodeBabyLink/虚拟网络，不标注为Tailscale；容器访问CookieCloud优先使用服务名，代理排除含相关服务名及100.64.0.0/10。
- 用户已配置的外部CookieCloud URL/KEY/密码不改；不能把控制台IP变更当成CookieCloud服务迁移。
- VNC维持`127.0.0.1:7900:7900`，不能为方便验收公开映射。通过SSH隧道浏览器验收。
- 绝不删现有目标、历史、CookieCloud数据、浏览器profile来提升成功率；诊断都用隔离副本。
- 不恢复JVM、Selenium Server、chromedriver、Python或Node作为运行依赖；若替代代理不能通过功能验收，应单独评估而非掩盖。
- 不因瘦身降低Chromium版本、禁用JavaScript/WebGL、删除中文字体或移除VNC/窗口管理器。
- 不运行UPX；不先换Alpine/musl；不盲删Mesa/LLVM/ICU/GTK/NSS/CA证书/时区数据。
- 每次只改一个逻辑面；失败先保留测试证据，再修复，不放松断言。
- 编译/测试不能用`command | head/tail`后的返回值充当成功依据。输出先写日志，再按原返回码判断。

## Task 1：冻结当前版本与建立可复现交付

**Files:** Create `deploy/compose.acceptance.yml`、`acceptance/baseline-manifest.json`、`acceptance/run.sh`；Modify `.dockerignore`；Use现有验收脚本及报告。

**Interfaces:** 输入生产镜像ID与隔离测试配置；输出带源码版本、镜像ID、二进制SHA256、包版本、大小的manifest，以及不含真实Cookie的用例结果。

- [ ] 在NAS记录基线，不改生产。当前ID为`sha256:00e0b6ea1e2a528a035a103dbd7fa697f0cde8bb8db375c4beb0a87981cec836`，948163640 bytes。

```sh
cd /volume1/docker/seltool-go
mkdir -p acceptance
BASE_ID=$(docker inspect seltool-go --format '{{.Image}}')
docker image inspect "$BASE_ID" > acceptance/image-inspect.json
docker exec seltool-go sha256sum /app/seltool /app/vncproxy > acceptance/binary-sha256.txt
docker run --rm --entrypoint dpkg-query "$BASE_ID" -W > acceptance/packages.txt
docker tag "$BASE_ID" seltool-go:rollback-20261018
```

`image-inspect.json`可能含环境信息，只保存在受限目录；对外manifest只提取ID/大小/基础版本，不粘贴全部内容。配置与数据备份含会话凭据，应限制权限；修改配置之前由NAS有权限的操作者备份config/data，禁止输出其内容。

- [ ] `.dockerignore`排除`config*/`、`data/`、`cookiecloud-data/`、`src/`、测试产物及日志；默认模板迁到非敏感静态目录后embed，避免排除config时把模板构建丢失。
- [ ] acceptance compose引用明确镜像ID，使用独立网络、账号、mock CookieCloud和profile；`run-on-startup: false`，不执行真实PT签到。
- [ ] 将46项既有用例输出为机器可读结果，VNC结果单独计数；当前预期是40过6失败而不是全绿。保存红灯基线后提交一次代码版本。

## Task 2：修复功能缺口，冻结功能通过版F1

**Files:** Modify `cookiecloud.go`、`settings.go`、`server.go`、`targets.go`、`templates.go`、`main.go`、`browser.go`、`import.go`、`web/index.html`；Create `acceptance/fixtures/`与对应Go测试文件。

**Interfaces:** 保留现有API路径/字段。远端诊断与运行缓存回退分离；TargetStore初始化/保存共享同一模板校验；浏览器登录结果只由实际证据确认。

- [ ] CookieCloud：为503/错误密码/404错误路径/200非法JSON/200无cookie_data/超时各建mock测试。`Probe`只请求远端，测试新输入时取请求配置、不落盘、不读取旧缓存；运行任务仍允许按配置回退。远端无法证明连通或解密时`ok:false`；404不能一概解释为“连接成功无数据”。日志不输出远端响应中的Cookie、密码或完整含key路径。原有空密码沿用、热更新、重启持久化必须继续通过。
- [ ] YAML与模板：没有JSON时用`cfg.Automation.Targets`初始化；存在JSON（包括空数组）优先JSON。模板ID必须存在；name/url/warmupPath等锁定字段及localStorage键集合/顺序按原作者规则校验，认证值必填。普通目标继续允许自由编辑，合法模板仍能保存。历史或已有数据不合法时给明确诊断，不静默覆盖/清空。
- [ ] 浏览器：在每个tab首次绑定后启用`Network.setBlockedURLs`，生成`*.ext`与`*.ext?*`，同时保留disable-images开关。用fixture证明PNG被拦、非目标WebP可加载。Cookie保留源domain及host-only语义；不把不匹配域改成另一个主机以强行登录；补`__Host-`、`__Secure-`、HTTPOnly、跨www与apex、重名Cookie测试。`loginPath`匹配当前URL而非标题。失败结果应分为网络、挑战、认证、未知，未知不得报成功。
- [ ] 启动与导入：Webhook覆盖配置加载失败、浏览器失败、CookieCloud告警和正常启动，保证通知发生顺序可解释。自动导入保留真实www主机名，预览后确认、与现有目标去重但不自动删除/修改；手动导入保留协议、路径、查询与片段。加错误域、重复目标、导入中失败不部分覆盖等测试。按后缀拦截、导入www以及Cookie域语义一项一提交。
- [ ] 在隔离环境运行46项回归及上述新增用例，检查失败断言全部转绿。站点验收保留全部32目标，不删除失败者；分开记录“软件通过”与“站点当前可用”。Cloudflare/雷池需人工处理的挑战明确标记，不承诺自动绕过。经确认后固定`seltool-go:functional-accepted`为F1并记录ID，后面的瘦身都与F1比较。

构建/测试模板（只针对隔离工作目录）：

```sh
set -eu
cd /volume1/docker/seltool-go
# 在隔离源目录执行，避免宿主机Go版本和noexec限制。
docker run --rm -v "$PWD/src:/src" -w /src \
  -e GOPROXY=https://goproxy.cn,direct -e CGO_ENABLED=0 \
  golang:1.26-alpine sh -ec 'go mod download; go test ./...; go build -trimpath -ldflags="-s -w" -o /src/seltool .; go build -trimpath -ldflags="-s -w" -o /src/vncproxy ./cmd/vncproxy' \
  > acceptance/build.log 2>&1
```

执行时固定构建器digest；不要依靠可漂移的`latest`或Docker build缓存猜测版本。构建失败保留日志与退出码，不复制旧产物部署。

## Task 3：补齐noVNC完整路径与生命周期验收

**Files:** Modify `cmd/vncproxy/main.go`、`deploy/entrypoint.sh`；Create `cmd/vncproxy/main_test.go`、`acceptance/vnc-browser-checklist.md`；Use `acceptance/vnctest.go`。

**Interfaces:** `/websockify`继续提供二进制RFB通道，`/vnc.html`和根页面入口不变；认证仍由x11vnc完成。仅代理实现与启动可靠性可改。

- [ ] 为TCP短写、非二进制消息、握手后后端不可达、读写任一方向断开加代理单测。TCP写必须循环直到完整消息发送，不能忽略`Write`返回的n<len(data)。关闭时两条转发协程必须退出；重复连接后连接数和goroutine数不得持续增长。
- [ ] 启动等待Xvfb和x11vnc真实就绪，而非只看到socket文件；用进程检查/端口探测限定等待时间，子服务失败应明确退出并进入容器重启策略。保留tini的信号转发和孤儿回收，验收docker stop在grace period内结束Chrome及后台服务。
- [ ] profile锁不能仅因为文件存在就无条件删除：先排除活跃Chrome与共享profile；不同验收实例禁止共用profile。测试正常重启、强停重建、NAS重启后的可恢复性。不能为过测删除用户profile。
- [ ] 用当前浏览器打开完整noVNC流程。工作电脑运行：

```sh
ssh -N -L 17900:127.0.0.1:7900 "user@10.0.0.3"
```

浏览器打开`http://127.0.0.1:17900/vnc.html`；正确/错误密码、画面、鼠标、中文/英文输入、窗口移动/缩放、断线重连逐项录证。剪贴板验证双向实际字符串，不把按钮存在当通过。原版与现版某能力都不支持时记录双方行为，而不是声称必须新增未定义功能。

- [ ] 协议13项重跑并完成浏览器清单，连续运行至少30分钟，20次连接/断开与连续3次重启无持续资源增长；停止后无遗留Chrome。达到要求才把Go代理视为可替代websockify。多浏览器差异单独列，不将协议通过扩大为所有端支持。

## Task 4：分级瘦身，一次一个候选

**Files:** Modify `deploy/Dockerfile.slim`；Create `deploy/Dockerfile.runtime-candidate`、`acceptance/compare-images.sh`；Modify构建入口与发布manifest。仅在Task2/3完成后执行。

**Interfaces:** 所有候选保留8080控制台/7900 noVNC、`/app/config`与`/app/data`挂载、原有环境变量。候选运行同一F1业务二进制，不能边瘦身边改业务逻辑。

### 4A 默认采用：低风险构建优化

- [ ] 多阶段构建生成strip后的静态Go二进制，使用`-trimpath -ldflags="-s -w"`；不使用UPX。源码/Go缓存/编译器只存在于builder，运行镜像不COPY。
- [ ] 固定Debian基础digest、Chromium与运行包版本、noVNC版本及源码归档校验和；将noVNC下载/裁剪放在独立fetch阶段，运行层不安装curl来取文件。COPY已验证的静态资产，保留浏览器运行要用的core/vendor/app/locale入口。
- [ ] apt安装与清理在同一RUN内完成：删除包索引、下载缓存、文档/手册；不删除时区和Chromium资源。每个二进制只COPY一次且COPY时设权限，避免“COPY后chmod”在后续层复制一份大文件。构建日志记录层大小。
- [ ] noVNC仅删除examples/tests/.git及不参与运行的工具，不删除vendor模块、翻译、客户端编码器或剪贴板相关文件。保留中文字体并检查中文显示；若能缩小字体，必须有覆盖证据，不能只测一个中文字符。
- [ ] F1与4A运行完整回归并计量，只有节省确实存在才接受。预计主要为数MB到数十MB，实际受F1构建组成影响；这不是700MB承诺。

参考构建方式（候选标签与生产分离）：

```sh
set -eu
cd /volume1/docker/seltool-go
docker build --pull=false -f deploy/Dockerfile.slim \
  -t seltool-go:candidate-safe . > acceptance/candidate-safe-build.log 2>&1
docker image inspect seltool-go:candidate-safe \
  --format '{{.Id}} {{.Size}}' > acceptance/candidate-safe-size.txt
docker history --no-trunc seltool-go:candidate-safe > acceptance/candidate-safe-history.txt
```

NAS已知BuildKit元数据拉取可能不走daemon代理；遇到该网络问题可先显式pull固定基础镜像，再用`DOCKER_BUILDKIT=0 --pull=false`。这是环境适配，不是关闭构建失败检查。

### 4B 条件采用：精确运行依赖镜像

- [ ] 4A仍不满足体积目标时，生成文件使用与依赖清单：包级dpkg-query、ELF ldd/readelf，以及运行时动态打开文件。覆盖浏览器启动、HTTPS/DNS、中文、JS/WASM/WebGL、截图、有界面VNC、重启，不只追踪一次首页。
- [ ] 在相同Debian/glibc版本基础上构建独立候选，保留动态加载器、NSS/DNS模块、证书、时区、字体/fontconfig、GTK运行数据、Chromium资源与VNC相关库。任何缺失.so或运行文件先回退，不用随机COPY整个/usr弥补。
- [ ] 首先排除明显未使用的构建/调试/测试文件，而不是拆GPU依赖。Mesa/LLVM存在apt依赖不等于每个文件都必需，但`ldd`也不能覆盖dlopen。若删除会影响WebGL、软件渲染或未知站点能力，即违反“保功能”，保留。
- [ ] 单独报告4B收益与维护代价。收益需实测，不承诺固定下限；如果只多省少量空间但破坏apt安全更新/依赖可追溯性，则拒绝4B、采用4A。
- [ ] 4B必须运行与4A相同的全部功能用例、30分钟稳定性与重启回归。用户未确认维护成本前，不把4B切为生产。

### 明确不采用的瘦身捷径

| 做法 | 不采用原因 |
|---|---|
| headless-shell替代完整Chromium | 不提供等价的有界面浏览器/VNC能力 |
| 换Alpine只看初始大小 | musl/字体/Chromium包差异扩大验证面，当前先不换 |
| 强制disable-gpu后删除Mesa/LLVM | CPU渲染仍可能依赖它，可能损失WebGL或站点兼容性 |
| 移除fluxbox或中文字体 | 会影响窗口操作、文字显示，不再等价 |
| 单纯后层rm文件 | Docker旧层仍占空间；可能表面干净但镜像不减 |
| 删除CookieCloud/目标/profile | 删除用户功能/数据，不属于镜像优化 |
| 把CookieCloud镜像大小计入应用大小 | 两个独立镜像，必须分开计量，不能混淆收益 |

本地`seltool-cookiecloud`可标为compose可选profile：仅连接外部CookieCloud时不启动本地服务，从而省运行资源。但这不减少seltool-go镜像本体，也不自动删除已有本地数据/容器。是否停用由用户明确决定。

## Task 5：发布、回滚及验收交接

**Files:** Modify `docker-compose.yml`与`README.md`；Create `acceptance/release-manifest.json`、`acceptance/release-result.md`。部署前由操作者备份compose和数据。

**Interfaces:** 输入F1与候选完整结果；输出可回滚的精确镜像ID、分开计量的大小与运行数据。IP只改变用户访问入口，不改变外部CookieCloud凭据。

- [ ] 统一展示/文档控制台地址`http://10.0.0.3:8081`；保留现有`8081:8080`映射，无需重启来“改IP”。VNC仍回环；NO_PROXY按真实访问路径保留虚拟网段/服务名。代理地址10.0.0.4:7890不要机械替换成10.0.0.3。
- [ ] 结果表至少记录：F1/候选的镜像ID、`docker image inspect .Size`、同格式压缩导出大小、峰值RSS、启动耗时、功能通过数、未验证项。Docker显示MB与压缩下载大小分开，不用宿主机剩余磁盘猜节省。
- [ ] 全部回归通过且无未确认关键退化，再备份compose，引用候选精确ID部署。用户站点与mock分别记结果，不用自动签到成功率证明无功能丢失。无授权不重跑真实PT签到。
- [ ] 发布后检查登录/设置/目标数据保留、CookieCloud拉取、VNC浏览器、一次经允许的单目标运行。若失败立即停止候选并用功能通过版F1恢复；原948MB是应急旧版本，仍有已知缺陷，不称“功能通过版”。
- [ ] 回滚时只换镜像，保留兼容配置/profile；迁移不兼容则停止候选后还原经保护备份。生产观察通过前不删F1和旧镜像、不执行docker system prune，不删除任何历史目标来“全绿”。

推荐回滚策略：compose上线引用保存下来的F1 ID，而不是可被覆盖的`:slim`标签；候选使用独立配置文件。profile只允许一个生产实例持有，蓝绿对测必须使用独立副本，避免Chrome锁冲突。

## 交付检查表

| 项目 | 完成条件 |
|---|---|
| 功能缺口 | 6项原有失败转绿，后缀拦截/通知/域名语义新增测试通过 |
| 核心链路 | HTTPOnly Cookie送达且localStorage生效，真实JS选择器判定 |
| 持久化 | 账户/目标/凭据/历史重启保持，不清用户profile |
| VNC | 协议13项+真实noVNC浏览器画面/输入/重连/既有剪贴板能力 |
| 稳定性 | 30分钟、20次连接/断开、3次重启，资源无持续增长 |
| 大小收益 | 与功能通过版F1比较，同版本同架构，输出字节和百分比 |
| 可回滚 | F1精确镜像ID、compose备份、兼容数据备份均可取得 |

## 推荐决策

**先Task1→2→3得到F1，再执行4A。4B是经收益与维护评估后的选择项，不是必做。**

接受“功能通过且只有小幅缩小”的结果；不接受“体积大幅缩小但某些站点/中文/VNC不可用”。当前948MB不是已知理论下限，亦没有证据保证全功能能压到180MB或700MB。
