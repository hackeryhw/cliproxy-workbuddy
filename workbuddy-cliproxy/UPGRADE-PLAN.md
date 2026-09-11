# workbuddy 插件升级计划（待实现）

> 记录时间：2026-09-11
> 状态：**已实现并部署（v0.2.0）**
> 目标：对接 9 个 CodeBuddy 接口（业务 7 + 签到 2）

## 实施结果（2026-09-11 19:25 验收）

| 项目 | 状态 |
|------|------|
| 插件版本 | **0.2.0** 已加载，无告警 |
| 模型数量 | **37 个**（静态兜底 31 + 动态贡献 6） |
| 签到控制台 | **HTTP 200**，地址 `http://127.0.0.1:8317/v0/resource/plugins/workbuddy/checkin` |
| 测试 | **27 项全部通过**（12 纯逻辑 + 7 假宿主链路 + 4 密封分支 + 1 浏览器端 + 4 真实上游联调） |
| 端到端 | `hy4-preview-f` 已输出 `reasoning_content`；`finish_reason` 已规范化为 null |

### 新增文件
- `upstream.go` —— 上游接口封装（配置/额度/签到 + host RPC 解包 + 凭据查找）
- `management.go` —— 管理路由注册 + 处理器 + 签到控制台 HTML
- `workbuddy_test.go` —— 27 项测试
- `scripts/verify-console-page.mjs` —— 浏览器端 JS 无头验证脚本

### 测试覆盖（22 项）
- 纯逻辑 12 项：路径分发、hy3/hy4 前缀、finish_reason 规范化、delta 清理、
  对话模型过滤、模型元数据构建、签到状态判定、业务码改写、注册契约、控制台页面
- 假宿主管理链路 7 项：凭据查找（含兜底与拒绝非本插件凭据）、状态处理器、
  签到幂等守卫、全路径路由分发
- 真实上游联调 4 项：签到状态、额度、动态模型目录、签到领取幂等

### 路由注册的直接证据
插件启动时 CPA 日志出现：
```
[host_callbacks.go:351] workbuddy console routes registered
```
这是宿主**确实调用 management.register 并接受我方路由**的直接证据
（此前只能从"资源页可访问"间接推断）。

### 控制台日志埋点
点击签到后，CPA 日志会按情况出现以下之一（便于排查）：
| 日志 | 含义 |
|------|------|
| `workbuddy console status served` | 状态查询成功 |
| `workbuddy quota served` | 额度查询成功 |
| `workbuddy check-in skipped: already claimed today` | 今日已签到（正常） |
| `workbuddy check-in claim finished` | 领取流程结束（含 ok 与 message 字段） |

### 密钥自动探测
控制台页面会自动从浏览器读取管理密钥，候选来源按序尝试并用 `/quota` 自验证：
1. `localStorage['cli-proxy-auth']` → zustand 持久化的 `state.managementKey`（当前版控制面板）
2. `localStorage['managementKey']` / `sessionStorage['managementKey']`（旧版明文槽位）
3. 其他名称含 `auth` 或值含 `managementKey` 的存储项
4. 本站自存槽位 `workbuddy-console-key`
5. 全部失败才提示手动粘贴

### 签到处理器分支覆盖（密封测试）
通过 `billingHTTPClient` 注入接缝 + 合成凭据，签到处理的每条分支都被覆盖：
| 分支 | 测试 |
|------|------|
| 上游返回成功 → ok=true，透传上游文案 | `TestClaimHandlerReportsUpstreamSuccess` |
| 上游返回已签到码 10001 → ok=false，改写为中文提示 | `TestClaimHandlerRelaysAlreadyClaimedCode` |
| 状态接口显示已签到 → 守卫短路，不发领取请求 | `TestClaimHandlerShortCircuitsWhenAlreadyCheckedIn` |
| 传输失败 → 502 + `claim_failed` | `TestClaimHandlerSurfacesTransportFailure` |
| 签到数据为空 → 状态查询仍可用 | `TestStatusHandlerToleratesEmptyCheckinData` |

### 浏览器端逻辑验证
`TestConsolePageBrowserLogic` 在 node:vm 里用桩 DOM/storage/fetch 执行**页面自己的 JS**，
断言它真的发出了正确的请求：
| 断言 | 内容 |
|------|------|
| 自动取密钥 | 从 `cli-proxy-auth` 的 zustand 状态里取出 `state.managementKey` |
| 候选自验证 | 用 `/quota` 探测该密钥 |
| 状态加载 | 带 `Authorization: Bearer <key>` 请求 `/checkin/status` |
| 渲染 | 账号、积分（1,600 格式化为 1,600）、本周进度、密钥框保持隐藏 |
| 点击签到 | 按钮触发 `POST /checkin/claim`，带正确头，按钮置灰 |
| 成功反馈 | 渲染 ok 样式消息 |

配套脚本 `scripts/verify-console-page.mjs` 也支持直接指向运行中的网关。

### 唯一未在本机验证的环节（已确认无替代路径）
仅剩 **浏览器 → CPA 自带管理鉴权中间件** 这一跳。
已查证 CPA 只接受 bcrypt 的 `remote-management.secret-key`：
- 无 `MANAGEMENT_PASSWORD` 环境变量（launchd 环境已检查）
- 无 local password（`X-Local-Password` 通道未启用）
- 无 `--local-password` 命令行参数

鉴权之后的全部逻辑已由假宿主 + 密封传输测试证明。

### 实施中发现并修复的两个额外 Bug
1. **动态模型列表静默降级**：`doJSON` 已拆开外层 `{code,msg,data}`，代码又按带 `data` 的结构解析，导致 `/v3/config` 解析出 0 个模型、静默退回静态清单。已修复并由 `TestLiveConfigModels` 加断言防回归。
2. **输出 token 上限被截断**：人为的 32768 上限会把 `hy4-preview-f` 合法的 64000 截断。已移除，改以上游数据为准。

## 一、接口对接清单（7 个）

### 已对接（5 个，保持不变）
| # | 方法 | 路径 | 用途 |
|---|------|------|------|
| 1 | POST | `/v2/plugin/auth/state?platform=CLI` | 申请登录态 |
| 2 | GET  | `/v2/plugin/login/account?state=` | 轮询扫码/取账号 |
| 3 | GET  | `/v2/plugin/auth/token?state=` | 换取 token |
| 4 | POST | `/v2/plugin/auth/token/refresh` | Token 续期 |
| 5 | POST | `/v2/chat/completions` | 对话主接口 |

### 待新增（2 个）
| # | 方法 | 路径 | 用途 | 备注 |
|---|------|------|------|------|
| 6 | GET  | `/v3/config` | 动态拉取真实模型列表 | 解决模型写死问题 |
| 7 | POST | `/billing/meter/get-user-resource-summary` | 查询积分余额 | 需带 `X-Domain` 头 |

### 明确不做
- ~~`/v2/billing/meter/get-dosage-notify`（额度告警）~~ —— **用户明确要求不做**

### 新增：每日签到（2 个，已实测验证 ✅）
| # | 方法 | 路径 | 用途 |
|---|------|------|------|
| 8 | POST | `/v2/billing/meter/checkin-activity-status` | 查签到状态（只读） |
| 9 | POST | `/v2/billing/meter/daily-checkin` | 执行签到（写） |

> 详见 `签到接口-已验证.md`

## 二、接口 6-7 的实现要点（已实测验证）

### 接口 6：动态模型列表
```http
GET /v3/config HTTP/1.1
Host: copilot.tencent.com
Authorization: Bearer <accessToken>
X-User-Id: <uid>
X-Domain: copilot.tencent.com
X-Product: SaaS
User-Agent: WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1
```
返回：
- `data.models` —— 面向用户的 50 个模型（含 `credits` 倍率、`reasoning.effort`、`onlyReasoning`）
- `data.agents[0].models` —— CLI agent 实际可调度的 15 个模型 ID

### 接口 7：积分余额
```http
POST /billing/meter/get-user-resource-summary HTTP/1.1
Host: copilot.tencent.com
Authorization: Bearer <accessToken>
X-User-Id: <uid>
X-Domain: copilot.tencent.com
Content-Type: application/json
User-Agent: axios/1.16.1

{}
```
返回 `data.Packages[]`：
- `CycleTotalCapacity` 总量 / `CycleRemainCapacity` 剩余 / `CycleUsedCapacity` 已用
- `CapacityUnit` = `credits`
- `IsPaidUser` 是否付费

## 三、main.go 待改的 4 处（已验证行号）

| # | 位置 | 改什么 | 严重度 |
|---|------|--------|--------|
| 1 | `main.go:81` | `clientUA` → `WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1` | 🟡 |
| 2 | `main.go:313-346` | `wbModels()` specs：10 个 → 24 个（或改为接口 6 动态拉取） | 🟠 |
| 3 | `main.go:1010-1020` | `forceMaxThinking()` 的 `HasPrefix(model,"hy3")` → 加上 `hy4` | 🔴 |
| 4 | `main.go:869-894` | `cleanChunkJSON()` 补 `choices[].finish_reason: "" → null` | 🟢 |

## 四、新增模型清单（实测全部 200 OK）

| 模型 ID | 名称 | 积分倍率 | 上下文 | 推理档 |
|---------|------|----------|--------|--------|
| `auto` | Auto | 浮动 | 168K | high |
| `fast-model` | 快速 | x0.21 | 300K | medium |
| `balanced-model` | 均衡 | x0.65 | 300K | medium |
| `deep-model` | 极致 | x1.20 | 300K | medium |
| `hy4-preview-f` | Hy4 preview | **x0.00** | 1M | - |
| `hy4-preview` | Hy4 preview | x0.29 | 1M | - |
| `hy3-x` | Hy3 | x0.05 | 192K | - |
| `glm-5.3` | GLM-5.3 | x0.79 | 1M | - |
| `glm-5.3-flash` | GLM-5.3-Flash | x0.06 | 1M | - |
| `kimi-k3-1` | Kimi-K3 | x1.62 | 1M | - |
| `minimax-m3` | MiniMax-M3 | x0.25 | 512K | medium |
| `kimi-k2.6` | Kimi-K2.6 | x0.52 | 256K | medium |
| `minimax-m2.7` | MiniMax-M2.7 | x0.26 | 200K | medium |
| `deepseek-v4.1-flash` | Deepseek-V4.1-Flash | x0.03 | 1M | high |

## 五、已实测确认不变的（无需改动）

- ✅ 对话路径 `/v2/chat/completions` 未变
- ✅ 非流式仍被拒：`code 11101 Non-stream chat request is currently not supported`
- ✅ SSE 分片结构未变：`chat.completion.chunk` + `delta.reasoning_content`
- ✅ 现有 10 个模型全部仍可用（含遗留别名 `hy3-preview` / `hy3-preview-agent` / `minimax-m3-pay`）
- ⚠️ SSE `delta` 新增两个可空字段：`refusal: ""`、`extra_fields: null`（现有 cleanChunkJSON 已能覆盖）
- ⚠️ `choices[].finish_reason` 返回 `""` 而非 `null`（需规范化）
