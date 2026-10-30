# 路线图（PM 视角）

本文件是这个分支的**迭代台账**：当前基线指标、已交付能力、以及按性价比排序的
PR 目标。规则很简单——每次只推一个 PR，落地后回写实测指标，再挑下一个。

## 基线指标（2026-10-30 实测，Windows 本机 / 部署机 ct-game.com）

| 指标 | 数值 | 测量方式 |
|---|---|---|
| 冷编译（清 cache，`-p 1`） | **40s** | `go clean -cache` 后 `go build ./cmd/server` |
| 热编译 | **3s** | 同上，不改源码重编 |
| linux/amd64 交叉编译 | **46.7s** / 产物 **9.43 MB**（引入 ECharts 后 10.43 MB） | `pwsh scripts/build.ps1 -Target linux` |
| 启动到首个健康响应 | **510ms**（3 次中位：610/509/510） | 起进程后轮询 `/healthz` 计时 |
| 账号池 | 8 个账号（cn + global 双域） | `/status` |
| 面板视图 | 9 个（账号池/仪表盘/令牌/用量/积分构成/任务中心/模型与档位/配置/运行日志） | 面板导航 |
| 部署耗时 | **~25s**（上传 + 重启 + 自检） | `pwsh scripts/deploy.ps1` |

> 启动已经不慢（510ms 里大半是进程加载与配置解析），所以**不再为启动做微优化**；
> 真正的痛点是「编译要记得加 `-p 1`」和「部署靠手敲」，本轮已用脚本固化。

## 已交付（按时间倒序）

| 提交 | 内容 | 价值 |
|---|---|---|
| 本轮 | `scripts/build.ps1` / `scripts/deploy.ps1` / 本文件 | 编译与部署各一条命令，消除"忘加 -p 1 就 OOM"和"部署漏自检" |
| 本轮 | **ECharts 仪表盘**（vendor 5.5.1 同源嵌入 1.03MB，动效+双轴+tooltip，SVG 兜底） | 图表可交互；实测修掉 canvas 高 0 的渲染 bug |
| 本轮 | **令牌持久化加固**：结构变更同步写、用量合并写（2s）、fsync + 退出 Flush | 每请求不再重写 tokens.json；立刻重启不丢用量 |
| 本轮 | **优雅退出**：`server.shutdown_timeout`（默认 15s）+ 主协程等 drain 完成 + systemd `TimeoutStopSec=30` | 重启不再硬切在途流式请求 |
| 本轮 | **前端渲染闸门**：`scripts/debug-panel.js` 扩到 9 视图 × 空/单条/异常三态（83 项断言），`go test` 直接跑它 | 改完不用肉眼看；改坏会红 |
| 本轮 | **归档下载**：`/panel/api/request_logs/{days,download}`（严格 day 校验防穿越、404 附可用日期） | 不用再 ssh 上去翻 JSONL |
| 本轮 | **配置报错可操作**：`parseDur` 统一 11 个时长字段，报错带字段名+当前值+示例 | 少一轮"改完再启动再失败" |
| 本轮 | **令牌级限流**：`rpm`（60s 滑动窗口）+ `max_concurrency`（在途闸门），超限 429 + `Retry-After`；面板可建/改/看 | 一个客户端打不满共享账号池 |
| 本轮 | **余额趋势**：`internal/balancehist` 采样（复用余额刷新节奏，JSONL 7 天保留）+ `/panel/api/balance_history` + 仪表盘堆叠面积图 | 能看出谁在掉积分、掉多快 |
| 本轮 | `scripts/api-smoke.ps1`（32 项接口探针） | 令牌配额/白名单/过期/删除、兼容层边界、鉴权、参数边界一次跑完 |
| 本轮 | Anthropic 错误信封修正 | 网关侧 401/403/429 改写成 `{type:error,error:{type:类别,code,message}}` |
| 本轮 | `scripts/debug-panel.js` | 无浏览器也能验渲染：vm + DOM 桩直接调 render 函数断言产出（令牌 11 项 / 仪表盘 8 项） |
| `1fb99e9` | 未实现端点回 501 + 端点清单 | 客户端不再把"本网关不提供该协议"误读成网关故障 |
| `1595ef8` | 多令牌（API Key）管理 | 对齐 one-api/new-api：多 key 独立启停/配额/白名单/用量 |
| `4be762e` | 面板「仪表盘」 | 在途流量 + 每分钟积分消耗 |
| `2729252` | 降级窗口改为「重试成功后锁定」+ 可配自愈 TTL | 一次误报不再污染全天；默认 30m 自愈 |
| `38d5802` | 到期提醒与选号同口径 + 同类型账号按最早到期优先 | 快过期积分不再白作废 |
| `3c64432` | Anthropic Messages 兼容层（`/v1/messages`） | Claude Code / cc-switch claude 通道直连 |
| `ba35265` | OpenAI Responses 兼容层（`/v1/responses`） | Codex 0.165+ 直连 |

## PR 目标（按性价比排序）

### P0 · 已有基础，收尾即可

**PR-1 启动自检与优雅退出**（小）—— ✅ 已交付（可配 shutdown_timeout + 主协程等 drain + systemd TimeoutStopSec）
- 现状：`systemctl stop` 后进程立即消失，在途流式请求被硬断。
- 做法：`SIGTERM` → 停止接新请求 → 等待在途请求（可配 15s 上限）→ 退出；
  systemd 单元加 `TimeoutStopSec`。
- 验收：压一个长流式请求时重启，客户端能收到完整 `[DONE]` 或明确错误，日志无 panic。

**PR-2 配置校验失败时给出可操作报错**（小）—— ✅ 已交付（时长类字段）
- 现状：`prompt.degrade_ttl` 之类写错会 fail fast，但只报字段名。
- 做法：错误信息带"合法取值示例"与"当前值"，与既有 `checkHourRange` 风格一致。
- 验收：故意写错 3 个字段，报错都能一眼看出怎么改。

### P1 · 能力补齐

**PR-3 令牌级限速（QPS/并发）**（中）—— ✅ 已交付（rpm 滑动窗口 + 并发闸门）
- 中转站标配：每把 key 独立限速，防止一个客户端打满账号池。
- 做法：令牌加 `rpm` 与 `max_concurrency` 字段，令牌维度令牌桶 + 在途计数；
  超限回 429 并带 `Retry-After`。
- 验收：单 key 压测超过 rpm 时稳定 429，其他 key 不受影响。

**PR-4 仪表盘 ECharts 化 + 余额趋势**（中）—— ✅ 已交付（ECharts 5.5.1 + 余额时序采样）
- 现状：仪表盘是手绘 SVG 柱状图，无动画；余额只有当前值。
- 做法：vendor `echarts.min.js`（同源嵌入，CSP 不用放宽）；后端加
  `usage` 之外的轻量余额时序快照（每 5 分钟采样到 `data/balance-history.jsonl`，
  保留 7 天），画 8 账号堆叠面积图。
- 验收：页面有入场动画与 tooltip；能看出"哪些号在掉积分、掉多快"。

**PR-5 `/v1/embeddings` 与未知端点明确 501**（小）—— ✅ 已交付（`1fb99e9`）
- 现状：未知路径 404 无提示，部分客户端把 404 当"网关坏了"。
- 做法：白名单外路径统一 `501 not_implemented`，body 里列出本网关支持的端点。
- 验收：`curl /v1/embeddings` 返回 501 + 端点清单。

### P2 · 工程卫生

**PR-6 面板前端冒烟测试扩展**（中）—— ✅ 已交付（9 视图 × 三态，83 项断言，接入 go test）
- 现状：`frontend_test.go` 只覆盖少数函数。
- 做法：给每个视图的 render 函数补最小断言（空数据/单条/异常数据三态）。
- 验收：故意把某个 render 函数改坏，测试能红。

**PR-7 请求归档的按天滚动 + 面板下载**（小）—— ✅ 已交付
- 现状：JSONL 归档只按容量淘汰，想看某天要 ssh 上去翻。
- 做法：`/panel/api/request_logs?download=1` 直接下载当日归档。
- 验收：面板点一下能拿到当天 JSONL。

## 调试手段

| 场景 | 手段 |
|---|---|
| 后端逻辑 / 协议 | `go test -p 1 ./...` + 线上 curl（`/healthz`、`/status`、目标端点） |
| 面板渲染（无浏览器） | `node scripts/debug-panel.js`（vm + DOM 桩，直接调 render 函数断言 HTML） |
| 面板真实交互 | `ego-browser` CLI（ego lite 0.6.1.13，本机装在 `C:\Users\Admin\.local\share\ego\active_version_dir\ego-browser.exe`，**不在 PATH**，写全路径）。PowerShell 必须写成 `.mjs` 再 `nodejs -e "await import('file:///...')"` |

### ego-browser 真机调试的四个坑（2026-10-30 实测）

1. `waitForFunction(fn, argument?, { timeout? })` —— **argument 在 options 之前**，顺序写反会报
   `options must be an object`。
2. `confirm()` 会阻塞 CDP：在 `page.evaluate()` 里同步点「删除」会抛
   `PageDialogOpenedError`，必须 `setTimeout(() => btn.click(), 0)` 把点击推到 evaluate
   返回之后，再 `page.acceptDialog()`。
3. 面板原先**没有 hashchange 监听**（只在启动读一次 hash），所以 `goto('#dashboard')`
   停在原视图——已修（本文件所在提交），现在前进/后退与脚本改 hash 都会切视图。
4. **ECharts 需要容器有确定高度**：`.uschart-body` 只有内边距，canvas 会渲染成 0 高
   （实测 `canvas h=0`，图表不可见）；SVG 路径自带 `height` 属性所以不受影响。
5. **视图隐藏时 init 拿到 0 尺寸**：切回该视图必须 `resize()` 一次（放 rAF 里等布局）。
6. **ECharts 分支里 `return` 会跳过后续渲染**：仪表盘主图命中 ECharts 后 `return`，
   放在兜底路径里的「余额趋势」调用因此被跳过（浏览器里整块空白）。离线 harness 只走
   SVG 兜底路径，**这类 bug 只有真机能发现**——所以每个新增图表都要真机过一遍。
7. **连续 `prompt()` 会串成两个对话框**：ego 里要逐个 `acceptDialog`，自动化易卡；对用户
   也啰嗦。改成单次输入 `rpm/并发`（如 `120/8`）后一次交互即可，且可粘贴。
8. `loadTokens()` 等刷新是异步的：断言列表前要 `waitForFunction` 等目标行出现，
   直接读 `innerText` 会读到上一帧（第一轮误判成"列表没刷新"）。

## 迭代循环（自监督）

1. **量化**：跑上表里的指标，与本文件基线比对。
2. **选题**：从 P0→P1→P2 里挑**一个** PR（不并行，避免 diff 互相打架）。
3. **实现**：`go build -p 1` / `go test -p 1 ./...` 全绿；补测试。
4. **验证**：`pwsh scripts/build.ps1 -Target linux` → `pwsh scripts/deploy.ps1`
   → 线上 `/healthz` + 目标接口实测。
5. **回写**：更新本文件「已交付」与基线指标；`git push fork main`。

> 内存约束（已验证两次）：任何 `go build` / `go test` 都必须 `-p 1`，否则
> `compile.exe: fork/exec ... The paging file is too small`。脚本已内置。
