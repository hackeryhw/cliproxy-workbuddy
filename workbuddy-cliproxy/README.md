# WorkBuddy（CLIProxyAPI 插件）

把腾讯 CodeBuddy（`copilot.tencent.com`）接到 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 上。CPA 对外提供 OpenAI / Anthropic 兼容接口，本插件负责扫码登录、刷新 token、拉模型目录，以及把对话转到上游。

这是对 [Sliverkiss/cpa-plugin](https://github.com/Sliverkiss/cpa-plugin) 公开 `workbuddy.so` 的 clean-room 重写，补了源码和 x86_64。WorkBuddy 的原始设计归属 Sliverkiss。

当前版本 **0.3.2**。

## 钉死的宿主

只在这一版 CPA 上验证过：

| 项 | 值 |
|----|----|
| CLIProxyAPI 二进制 | **7.2.149**，commit **`2a6b87ac`**，BuiltAt `2026-09-03T13:31:22Z` |
| 本插件编译用的 SDK | `github.com/router-for-me/CLIProxyAPI/v7` **v7.2.30**（见 `go.mod`） |

不要把 CPA 升到更新的 7.x。登录、凭证文件、管理面板都可能直接坏掉。

本机和 CPA 放在同一目录时，从仓库根目录看说明：[上层 README](../README.md)。

## 目录

| 文件 | 作用 |
|------|------|
| `main.go` | 插件入口、RPC 分发、认证、执行器、请求头 |
| `rotate.go` | **多账号轮换**（错误分类、凭据池、轮换决策） |
| `upstream.go` | 上游调用（凭据枚举、计费、模型目录） |
| `management.go` | 管理 API 与控制台路由注册 |
| `console_page.go` | 独立签到页（`/v0/resource/plugins/workbuddy/checkin`） |
| `growth_tasks.go` / `growth_desktop.go` | 成长中心任务自动化、桌面端埋点 |
| `panel/workbuddy-panel.js` | 面板注入脚本（配额卡片、签到按钮） |
| `workbuddy_test.go` / `rotate_test.go` / `headers_test.go` / `growth_tasks_test.go` | 测试 |

## 多账号

每个 Buddy 账号一份凭据，登录后写入 `~/.cli-proxy-api/workbuddy-<uid>.json`。已经存在的 `workbuddy.json` 会继续用。

- 再扫一个**新**号：面板里再添加一次 WorkBuddy 凭据，不会覆盖已有账号
- 再扫**同一个**号：只更新那份文件
- 额度页和签到页按账号分卡

### 从官方 App 导入账号

`WorkBuddy.app` 已登录的号可以直接搬过来，不用重新扫码：

```bash
bash scripts/import-official-account.sh
```

读的是 `~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`，写 `~/.cli-proxy-api/workbuddy-<uid>.json`（权限 `0600`）。同一个 uid 已存在会拒绝（退出码 2），不会覆盖也不会重复。

登录一个**新**号 → 跑一次脚本 → 循环。凭据里的 `domain` 是**登录域**（token 的 issuer），脚本原样搬运：

- `copilot.tencent.com` —— 旧的 `.com` 体系
- `www.workbuddy.cn` —— WorkBuddy App 的新体系

两者都受支持，混在同一个池子里没问题，插件会把 `domain` 如实透传成 `X-Domain`（见下面「渠道身份」）。

### 自动轮换账号

宿主每次只给插件**一个**凭据，失败后不会自己换下一个——因为插件执行器返回的错误是普通 `error`，宿主读不到 HTTP 状态码（`statusCodeFromError` 需要 `StatusCode() int` 接口，拿不到就当 0，于是不重试）。

所以换号由插件自己做（`rotate.go`）：

- **只在第一个 chunk 发出前换号。** 之前客户端只收到响应头，换号对它是不可见的；一旦有 chunk 发出，响应就提交了，此时失败只报错不重试——否则会把两次上游尝试缝进同一个流里。
- **最多烧 3 个凭据**（`maxRotateAttempts`），间隔 250ms。目的是扛过某个号挂了/被限流，不是在上游全挂时硬刷整池。
- **可重试的失败**：
  - `403 / 408 / 429 / 500 / 502 / 503 / 504`
  - **`HTTP 400` + 业务码 `11128`**（`Illegal API invocation from an unapproved channel`）——看着像请求错，其实是账号/渠道维度的拒绝，换个号可能就过了
- **不重试**：`400 / 401 / 404 / 422` 等请求本身的错——每个号都会一样地失败，换号只会放大上游压力

触发时日志会打：

```
workbuddy rotation: switching account reason={"code":11128,"msg":"Illegal API invocation from an unapproved channel",...}
```

> 说明：插件上报的 `fields` 里其实带了 `from` / `to` / `status` / `attempt`，但宿主日志只渲染 `reason` 这一个键，所以**看日志只能看到失败原因，看不到换到了哪个号**。想定位换号结果，配合上游调用后的成功/失败记录一起看。

### 额度与限流

每个号有独立的额度/频率限制。上游的业务错误有的走 HTTP 状态码，有的走响应体里的 `code` 字段。常见：

| `code` | 含义 | 是否换号 |
|--------|------|----------|
| `6004` | 使用量超出频率限制（响应里给出重置时间） | 视 HTTP 状态码而定（见下） |
| `11101` | 拒绝非流式请求 | 否（插件内部已改为流式，正常遇不到） |
| `11128` | 渠道未批准 | **是**（按业务码特判，见上） |

`6004` 的 HTTP 状态码上游给的是限流类（`429`）还是 `200`，取决于具体网关实现；实测日志里它确实触发了换号。想确认某个版本的行为，看日志里那条 `rotation: switching account` 有没有出现。

## 安装

需要：已经在跑的 CLIProxyAPI **7.2.149**（带 CGO / 插件支持）、CodeBuddy 账号、Go 1.26+ 与 gcc。编译架构必须和 CPA 一致（本机是 darwin/arm64）。

本机（插件源码和 CPA 在相邻目录）：

```bash
bash scripts/install-local.sh
```

脚本会编译 `workbuddy.dylib`、拷进旁边的 `plugins/`，并重启 `local.cliproxyapi`。插件目录的判定顺序：`$CPA_PLUGINS` → 仓库上一级的 `plugins/` → `~/Projects/Gemini API/plugins`。

单独编译：

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o workbuddy.dylib .
```

产物放到 CPA 的 `plugins/`，并在 `config.yaml` 启用：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy: { enabled: true, priority: 100 }
```

重启 CPA。日志出现 `plugin loaded ... plugin_id=workbuddy` 和 `version=0.3.2` 即成功。`GET /v1/models` 里应能看到 WorkBuddy 的模型。

改完 `.dylib` 必须重启：CPA 的文件监听只跟踪配置和凭据目录，不会热加载插件。

```bash
launchctl kickstart -k gui/$(id -u)/local.cliproxyapi
```

替换现网插件前先备份（`workbuddy.dylib` 被 `*.dylib` 规则忽略，不进 git）：

```bash
cp "$PLUGIN_DIR/workbuddy.dylib" "$PLUGIN_DIR/workbuddy.dylib.bak-$(date +%Y%m%d-%H%M%S)"
```

然后到面板添加 WorkBuddy 凭据并扫码。要第二个账号，再添加一次。

## 给客户端用

CPA 默认端口 `8317`。调用密钥是 CPA `config.yaml` 里的 `api-keys`，不是 CodeBuddy 密码。

| 协议 | Base URL |
|------|----------|
| OpenAI | `http://<host>:8317/v1` |
| Anthropic | `http://<host>:8317`（不要带 `/v1`，走 `x-api-key`） |

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=<api-key>
export ANTHROPIC_MODEL=auto
claude
```

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

流式和非流式都可以。非流式会在插件内部改成流式再聚合（上游 `code 11101` 拒绝非流式）。

## 管理 API

面板 key 鉴权（`Authorization: Bearer <management key>`），挂在 `/v0/management` 下：

| 方法 | 路径 | 上游调用 | 用途 |
|------|------|----------|------|
| GET | `/plugins/workbuddy/accounts` | 0 次 | 账号列表（画卡片框） |
| GET | `/plugins/workbuddy/accounts/status` | 每号 2 次 | 额度 + 签到态（带 `auth_index` 则只查该号） |
| GET | `/plugins/workbuddy/quota` | 1 次 | 定向查额度 |
| GET | `/plugins/workbuddy/checkin/status` | 1 次 | 定向查签到状态 |
| POST | `/plugins/workbuddy/checkin/claim` | 1–2 次 | 定向签到 |
| POST | `/plugins/workbuddy/refresh` | 1 次 | 轮换凭证并写回 |
| POST | `/plugins/workbuddy/tasks/auto_all` | 多次 | 一键做成长中心任务 |

资源路由（免鉴权，供面板 iframe 嵌入）：

```
GET http://<host>:8317/v0/resource/plugins/workbuddy/checkin
```

## 签到与积分

三个入口，功能等价：

1. **面板「插件」分组（推荐）**  
   侧边栏会出现「WorkBuddy 签到」，页面用 iframe 嵌入。这条路不改面板，面板自动更新可以开着。

2. **面板「配额管理」页**  
   每个账号一张卡片。卡片框马上出现，额度要点「点击此处刷新额度」才拉（每账号两次上游往返）。

3. **面板「认证文件」页**  
   每张凭证上有积分、签到、刷新凭证。刷新由插件自己调上游并写回文件，不是把凭证标成过期交给宿主。宿主只给 `codex / claude / antigravity / kimi / xai` 注册了 refresh lead，动态插件加不进去。

独立页面：`http://<host>:8317/v0/resource/plugins/workbuddy/checkin`

配额页和认证文件页的卡片需要往面板注入 `scripts/inject-panel.mjs`（不改压缩代码），并关闭面板自动更新：

```yaml
remote-management:
  disable-auto-update-panel: true
```

想开机后自动补注入：`bash scripts/install-panel-inject-agent.sh`。细节见 [PANEL-INTEGRATION.md](PANEL-INTEGRATION.md)。

插件页面读不到面板存在加密存储里的管理密钥。它和面板同源，会嗅探面板自己发出的请求头。密钥始终留在浏览器里。拿到密钥之前一个请求都不发——CPA 管理鉴权连续失败 5 次会封 30 分钟。

## 模型

列表由上游 `GET /v3/config` 动态拉取（缓存 10 分钟），没有结果时用内置静态表：

```text
auto · fast-model · balanced-model · deep-model
hy4-preview-f · hy4-preview · hy3 · hy3-x · hunyuan-chat
glm-5.3 · glm-5.3-flash · glm-5.2 · glm-5.1 · glm-5.0-turbo · glm-5v-turbo · glm-4.6v
kimi-k3-1 · kimi-k2.7 · kimi-k2.6 · kimi-k2.5 · kimi-k2-thinking
deepseek-v4.1-flash · deepseek-v4-pro · deepseek-v4-flash · deepseek-v3-2-volc
minimax-m3 · minimax-m2.7 · minimax-m2.5
```

`auto` / `fast-model` / `balanced-model` / `deep-model` 是路由别名：上游按请求挑具体模型，响应体的 `model` 会给出真实名字。显示名会带腾讯的积分倍率。能不能用，以该 Buddy 账号的权限为准。

混元（`hy3` / `hy4` / `hunyuan-*`）会强制 `reasoning_effort=high`。思考内容走 SSE 的 `delta.reasoning_content`。

## 渠道身份（重要）

上游网关会从**请求头**判断调用方是不是"被批准的渠道"。官方 WorkBuddy 桌面客户端每条对话请求都会带这些头（见 `cli/dist/codebuddy.js` 的 `axiosToFetchAdapter`）：

```text
X-IDE-Name: WorkBuddy          X-Agent-Intent: craft
X-IDE-Type: WorkBuddy          X-Agent-Type:   main
X-IDE-Version: 5.5.6           x-codebuddy-request: 1
X-Request-ID · X-Conversation-ID · X-Conversation-Message-ID · X-Conversation-Request-ID
```

**少任何一个都可能被拒**，表现为：

```
HTTP 400  {"code":11128,"msg":"Illegal API invocation from an unapproved channel", ...}
```

插件在 `applyChannelIdentityHeaders()` 里补齐这些头，关联 ID 每请求新生成。`X-Domain` 则**如实透传**凭据自己的登录域（`.cn` 号发 `.cn`、`.com` 号发 `.com`）——这正是官方客户端的行为，把它写死成 `copilot.tencent.com` 是错的。

## 上游接口

| # | 方法 | 路径 | 用途 |
|---|------|------|------|
| 1 | POST | `/v2/plugin/auth/state?platform=CLI` | 申请登录态 |
| 2 | GET | `/v2/plugin/login/account?state=` | 轮询扫码、取账号 |
| 3 | GET | `/v2/plugin/auth/token?state=` | 换 token |
| 4 | POST | `/v2/plugin/auth/token/refresh` | token 续期 |
| 5 | POST | `/v2/chat/completions` | 对话（流式） |
| 6 | GET | `/v3/config` | 动态模型目录 |
| 7 | POST | `/billing/meter/get-user-resource-summary` | 积分余额 |
| 8 | POST | `/v2/billing/meter/checkin-activity-status` | 签到状态 |
| 9 | POST | `/v2/billing/meter/daily-checkin` | 每日签到 |

计费接口（7–9）都是 POST + `{}`，必须带 `X-Domain: copilot.tencent.com`。接口 7 在裸 `/billing` 下，没有 `/v2`。

## 测试

```bash
CGO_ENABLED=1 go test ./...
```

没有本机 Buddy 凭据时，真实上游测试会跳过。没有 node 时，浏览器端测试会跳过。

轮换和请求头这两块有专门的回归测试：

- `rotate_test.go` —— 错误分类（含 `11128` 必须换号、普通 400 必须不换）、凭据池跳过、流式轮换四种状态
- `headers_test.go` —— 渠道身份头契约、关联 ID 每请求唯一、`X-Domain` 透传

## 排障

| 现象 | 原因 / 处理 |
|------|-------------|
| `400 code 11128` | 渠道身份头缺失或被拒。确认跑的是含 `applyChannelIdentityHeaders()` 的版本；仍然报错就把响应体原样留档 |
| `code 6004` | 该号频率/用量超限，响应里有重置时间。通常会自动换号（看日志有无 `rotation: switching account`） |
| 日志 `context deadline exceeded`（model registrar） | 拉 `/v3/config` 超时，通常不影响插件注册；确认网络可达上游 |
| 改完插件行为没变 | `.dylib` 不会热加载，必须重启 CPA |
| 面板只显示 UUID 不显示昵称 | 看的是宿主原生「认证文件」页；插件注入的卡片会显示昵称 |

抓官方 App 的真实流量（验证上游行为时用）：

```bash
bash start-workbuddy-via-burp.sh
```

强制 Env 代理 + Electron `--proxy-server`，并注入 Burp CA。注意：**部分后台通道不走代理**，抓不到模型请求本身（该 App 的对话连接会直连上游），别据此误判"请求没发出"。

## Claude Code 和流式

CodeBuddy 把 Claude Code 两句固定 system 模板逐字加进了黑名单。插件转发前会做最小改写（`CLI` → `CLI tool`，`Main branch` → `Default branch`）。腾讯若再加模板句，要改 `sanitizeBlockedTemplates`。

对话是真流式：边读上游边 `host.stream.emit`。转发前还会把空的 `finish_reason` 改成 `null`，并删掉 `delta` 里的空字段，避免严格客户端解析失败。SSE 注释行（例如 `: heartbeat`）会丢掉，不往下传。

## License

MIT。
