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

## 多账号

每个 Buddy 账号一份凭据，登录后写入 `~/.cli-proxy-api/workbuddy-<uid>.json`。已经存在的 `workbuddy.json` 会继续用。

- 再扫一个**新**号：面板里再添加一次 WorkBuddy 凭据，不会覆盖已有账号
- 再扫**同一个**号：只更新那份文件
- 额度页和签到页按账号分卡

## 安装

需要：已经在跑的 CLIProxyAPI **7.2.149**（带 CGO / 插件支持）、CodeBuddy 账号、Go 1.26+ 与 gcc。编译架构必须和 CPA 一致（本机是 darwin/arm64）。

本机（插件源码和 CPA 在相邻目录）：

```bash
bash scripts/install-local.sh
```

脚本会编译 `workbuddy.dylib`、拷进旁边的 `plugins/`，并重启 `local.cliproxyapi`。

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

## Claude Code 和流式

CodeBuddy 把 Claude Code 两句固定 system 模板逐字加进了黑名单。插件转发前会做最小改写（`CLI` → `CLI tool`，`Main branch` → `Default branch`）。腾讯若再加模板句，要改 `sanitizeBlockedTemplates`。

对话是真流式：边读上游边 `host.stream.emit`。转发前还会把空的 `finish_reason` 改成 `null`，并删掉 `delta` 里的空字段，避免严格客户端解析失败。SSE 注释行（例如 `: heartbeat`）会丢掉，不往下传。

## License

MIT。
