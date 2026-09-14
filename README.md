# CLIProxyAPI + WorkBuddy

本机用 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）开一个 HTTP 接口，再用 WorkBuddy 插件把腾讯 [CodeBuddy](https://www.codebuddy.cn) 接到这个接口上。Cursor、Claude Code、Cline、curl 都可以当客户端。

这是私有仓库：源码、配置模板、启动脚本。二进制、调用密钥、Buddy 登录态都不在里面。

## 钉死的版本

WorkBuddy **0.3.2** 只在这一对组合上验证过。CPA 小版本一变，插件 ABI、扫码登录、凭证文件名、管理面板都可能对不上。**不要用最新包，也不要点面板里的升级。**

| 组件 | 钉死的版本 | 怎么核对 |
|------|------------|----------|
| CLIProxyAPI 二进制 | **7.2.149**，commit **`2a6b87ac`**，BuiltAt `2026-09-03T13:31:22Z` | `bin/cli-proxy-api -h` 第一行必须整句对上 |
| WorkBuddy 插件 | **0.3.2**（本地 `plugins/workbuddy.dylib`） | 日志 `plugin registered ... version=0.3.2` |
| 插件编译用的 SDK | `github.com/router-for-me/CLIProxyAPI/v7` **v7.2.30** | `workbuddy-cliproxy/go.mod` |

核对二进制（三个字段都要在，缺一个就别启动）：

```text
CLIProxyAPI Version: 7.2.149, Commit: 2a6b87ac, BuiltAt: 2026-09-03T13:31:22Z
```

```bash
./bin/cli-proxy-api -h 2>&1 | head -1
```

`scripts/download.sh` 默认只拉 `v7.2.149`。指定别的版本会直接拒绝；下完还会核对 commit，对不上就把文件删掉。真要换宿主，先改插件并重新验证，再改脚本里的钉死值和上表。

## 仓库里有什么

```text
README.md                 本说明
config.example.yaml       配置模板（不含密钥）
scripts/                  下载 CPA、装后台服务、登录辅助
static/management.html    管理面板页面（已注入 WorkBuddy 配额卡片）
workbuddy-cliproxy/       WorkBuddy 插件源码（签到、配额、账号轮换）
stealth-plugin/           Stealth 插件源码（请求特征清洗，见下）
bin/README.md             说明为什么 bin/ 不放二进制、怎么拉
docs/                     本机部署的术语和 ADR
```

**不会进仓库、必须留在这台机器上：**

| 路径 | 内容 |
|------|------|
| `bin/cli-proxy-api` | CPA 本体，用 `scripts/download.sh` 拉 7.2.149（见 `bin/README.md`） |
| `plugins/*.dylib` | 两个插件的编译产物，由各自的 `build.sh` / `install-local.sh` 生成 |
| `config.yaml` | 调用密钥和管理密钥，从模板复制后自己填 |
| `~/.cli-proxy-api/` | Buddy 登录态（`workbuddy.json` / `workbuddy-<uid>.json`）和日志 |

## 两个插件分别是干什么的

| 插件 | 类型 | 作用 |
|------|------|------|
| **workbuddy** | ModelProvider + AuthProvider + Executor | 把腾讯 CodeBuddy 接到 CPA：扫码登录、刷新 token、拉模型目录、转发对话、多账号轮换 |
| **stealth** | RequestInterceptor | 改写各客户端内置的「身份特征句」，让上游的精确匹配失效（详见 `stealth-plugin/README.md`） |

两者互不依赖，可以单独启用。`config.yaml` 里 `stealth` 的 `priority` 高于 `workbuddy`——拦截器要先于执行器看到请求。


## 克隆之后怎么用

需要：macOS、Go 1.26+、gcc（编译插件用）、能登录 CodeBuddy 的账号。把下面的 `ROOT` 换成你放仓库的目录。本机现在用的是 `/Users/haoweiyang/Projects/Gemini API`。

**换一台电脑，最短路径：**

```bash
git clone https://github.com/hackeryhw/cliproxy-workbuddy.git
cd cliproxy-workbuddy

CLIPROXY_VERSION=v7.2.149 bash scripts/download.sh        # 1. 拉 CPA 本体（57MB，不入库）

cp config.example.yaml config.yaml                        # 2. 写配置，然后自己改 api-keys / secret-key

cd workbuddy-cliproxy && bash scripts/install-local.sh    # 3. 编译并部署 WorkBuddy 插件
cd ../stealth-plugin   && bash build.sh                   # 4. 编译并部署 Stealth 插件

cd .. && bash scripts/install-service.sh                  # 5. 装后台服务（只需一次）
```

然后打开 <http://127.0.0.1:8317/management.html> 登录 Buddy 账号即可（详见下文）。

> **第 2 步不能跳过**：`install-service.sh` 会检查 `config.yaml` 是否存在，没有就直接退出。里面的 `api-keys` 和 `remote-management.secret-key` 也必须在启动前改掉。
>
> 三个 `build/install` 脚本都会自己重启 `local.cliproxyapi`（它没在跑就跳过）。第 3、4 步顺序无所谓，但**改完插件必须重启 CPA 才生效**——它不会热加载 `.dylib`。

下面是每一步的展开说明。

```bash
ROOT="$PWD"
```

### 1. 下载 CPA（只能是 7.2.149）

```bash
CLIPROXY_VERSION=v7.2.149 bash scripts/download.sh
./bin/cli-proxy-api -h 2>&1 | head -1
```

第一行必须和上面那句 `Version / Commit / BuiltAt` 完全一致。对不上就删掉 `bin/cli-proxy-api`，不要启动。

### 2. 写配置

```bash
cp config.example.yaml config.yaml
```

打开 `config.yaml`，至少改：

- `api-keys`：一长串随机字符串。这是**客户端调 CPA** 时带的密钥，不是 CodeBuddy 密码。
- `remote-management.secret-key`：再换一串，给管理面板用。第一次启动后 CPA 会把它收成哈希。
- `plugins.configs.workbuddy.enabled: true`
- `remote-management.disable-auto-update-panel: true`（配额页的 WorkBuddy 卡片靠注入，官方面板更新会盖掉）

`auth-dir` 保持 `~/.cli-proxy-api`。`config.yaml` 不要提交。

如果仓库不在 `/Users/haoweiyang/Projects/Gemini API`，还要改 `scripts/local.cliproxyapi.plist` 里的绝对路径，再执行下一步。

### 3. 编译 WorkBuddy 插件

```bash
cd workbuddy-cliproxy
bash scripts/install-local.sh
```

脚本会编译 `workbuddy.dylib`、拷到旁边的 `plugins/`，并尝试重启 `local.cliproxyapi`。日志里应出现：

```text
plugin loaded plugin_id=workbuddy
plugin registered plugin_id=workbuddy ... version=0.3.2
```

### 3b. 编译 Stealth 插件

```bash
cd stealth-plugin
bash build.sh
```

产物是 `stealth.dylib`，同样拷进 `plugins/` 并重启服务。它是个 `RequestInterceptor`，只改写请求体里的特征句，不碰路由和凭据——启用与否不影响 WorkBuddy 的登录和账号轮换。清洗规则见 `stealth-plugin/README.md`。

两个脚本都支持 `CPA_PLUGINS` 覆盖插件目录。插件目录的判定顺序是：`$CPA_PLUGINS` → 仓库根下的 `plugins/`。

### 4. 装成后台服务（只需一次）

```bash
bash scripts/install-service.sh
```

之后开机自启，监听 **8317**。停掉：`bash scripts/uninstall-service.sh`。前台临时跑：`bash scripts/start.sh`。

### 5. 登录 Buddy 账号

浏览器打开 <http://127.0.0.1:8317/management.html>（只本机可开）。在认证文件 / OAuth 里添加 **WorkBuddy**，用 CodeBuddy 扫码。

- 每个 Buddy 账号扫一次，各写一份 `workbuddy-<uid>.json`
- 已经存在的 `workbuddy.json` 会继续用，不会被改名
- 同一账号再扫一次，只更新那份文件
- 换另一个号再添加一次凭据，不会覆盖已有账号

侧边栏 **插件 → WorkBuddy 签到** 可以看额度和每日签到。配额管理页选「全部」也会出现 WorkBuddy 卡片。

## 已经在跑的时候

1. 打开 <http://127.0.0.1:8317/management.html>
2. 需要第二个 Buddy 账号时，再添加一次 WorkBuddy 并扫码
3. 客户端按下表填写

| 协议 | Base URL | 密钥 |
|------|----------|------|
| OpenAI 兼容 | `http://127.0.0.1:8317/v1` | `config.yaml` 的 `api-keys`，Header 用 `Authorization: Bearer …` |
| Anthropic 兼容 | `http://127.0.0.1:8317`（不要带 `/v1`） | 同一把密钥，Header 用 `x-api-key` |

模型名以面板或 `GET /v1/models` 为准，例如 `auto`、`hy4-preview-f`、`glm-5.3`。混元系列会强制开深度思考。

```bash
curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8317/management.html

# 把 KEY 换成 config.yaml 里的 api-keys
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

Claude Code：

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_API_KEY=<config.yaml 的 api-keys>
export ANTHROPIC_MODEL=auto
claude
```

局域网里其他设备把 `127.0.0.1` 换成这台 Mac 的 IP，端口仍是 `8317`。管理面板默认不允许远程访问。

## 日常维护

| 要做的事 | 命令 |
|----------|------|
| 重启 CPA | `launchctl kickstart -k gui/$(id -u)/local.cliproxyapi` |
| 改完插件源码后生效 | 在 `workbuddy-cliproxy/` 里再跑 `bash scripts/install-local.sh`（dylib 不会热更新） |
| 看插件是否加载 | `rg "plugin registered plugin_id=workbuddy" ~/.cli-proxy-api/logs/cliproxyapi.stdout.log`（必须是 0.3.2） |
| 核对 CPA 版本 | `./bin/cli-proxy-api -h 2>&1 \| head -1`（必须是 7.2.149 / `2a6b87ac`） |
| 加 / 换 Buddy 账号 | 面板里再走一遍 WorkBuddy 扫码 |

改 `config.yaml` 一般会热加载；只换 `plugins/*.dylib` 必须重启。

CodeBuddy 上游只接受流式对话。CPA 对外可以非流式，插件会在内部改成流式再聚合。模型是否能用，以该 Buddy 账号在 CodeBuddy 里的权限为准。插件协议见 [`workbuddy-cliproxy/README.md`](workbuddy-cliproxy/README.md)。

## 不要做的事

- **不要升级 CLIProxyAPI。** 不要跑未钉死版本的 `download.sh`，不要换 GitHub 上的最新 release，不要点管理面板里的更新。宿主不是 7.2.149 / `2a6b87ac` 时，先停服务。
- 不要把 `config.yaml`、`~/.cli-proxy-api/*.json`、Burp 证书提交或发给别人。
- 不要对管理面板乱猜密钥：连续失败 5 次会封 30 分钟。
- Gemini CLI / Antigravity 登录脚本还在 `scripts/` 里，但当前配置里 `gemini-cli` 是关掉的。要用 Gemini 时再打开对应 plugin，并跑 `scripts/login.sh` 或 `scripts/login-gemini-cli.sh`。
