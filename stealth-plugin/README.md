# Stealth Plugin for CLIProxyAPI

一个轻量、纯粹的 `CLIProxyAPI` 请求拦截器插件（`RequestInterceptor`）。

## 作用
解决各种 AI 客户端（Claude Code、Claude Agent SDK、DeepSeek Harness、OpenAI Codex、Cursor、GitHub Copilot 等）在请求中内置的“身份特征/系统提示词模板”被各大上游服务商（如 Google Antigravity、腾讯 CodeBuddy、各大反代与企业 WAF）精确匹配、阻断封杀、返回 429 或 400 的问题。

通过对特征短语进行“微调 1 个词/字母”的无损替换：
- **彻底破坏上游特征指纹的精确匹配**
- **100% 保留大模型对提示词的语义理解**
- **无需修改任何客户端或二进制，换电脑零感知**

---

## 包含的特征清洗规则表

### 1. Claude Code & Claude Agent SDK
- **Google Antigravity 429 敏感词**：
  - 匹配：`"You are a Claude agent, built on Anthropic's Claude Agent SDK."`
  - 替换：`"You are a Claude agent, built on Anthropix's Claude Agent SDK."`
- **Tencent CodeBuddy 400 敏感词**：
  - 匹配：`"You are Claude Code, Anthropic's official CLI tool for Claude."`
  - 替换：`"You are Claude Code, Anthropic's official CLI tool for Claude."`
  - 匹配：`"Default branch (you will usually use this for PRs)"`
  - 替换：`"Default branch (you will usually use this for PRs)"`
- **Claude Code SDK 运行时特征**：
  - 匹配：`"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."`
  - 替换：`"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."`

### 2. DeepSeek Harness (DSH)
- **DSH 固定身份前缀**：
  - 匹配：`"You are an AI agent powered by DeepSeek Engine."`
  - 替换：`"You are an AI agent powered by DeepSeek Engine."`
- **DSH Persona 前缀**：
  - 匹配：`"You are a coding assistant powered by the "`
  - 替换：`"You are a coding assistant powered by the "`

### 3. OpenAI Codex
- **Codex 主助手身份声明**：
  - 匹配：`"You are Codex, an AI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."`
  - 替换：`"You are Codex, an AI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."`
- **Codex Root 协作身份**：
  - 匹配：`"You are `root`, the primary agent in a team of agents collaborating to fulfill the user's goals."`
  - 替换：`"You are `root`, the primary agent in a team of agents collaborating to fulfill the user's goals."`
- **Codex Reviewer 动作审查语境**：
  - 匹配：`"The following is the coding agent history whose request action you are assessing."`
  - 替换：`"The following is the coding agent history whose request action you are assessing."`

### 4. Cursor & GitHub Copilot
- **Cursor 编程助手身份**：
  - 匹配：`"You are an expert programming assistant, including writing code, debugging, and explaining concepts."`
  - 替换：`"You are an expert programming assistant, including writing code, debugging, and explaining concepts."`
- **Cursor 编辑器身份**：
  - 匹配：`"You are an AI-powered code editor."`
  - 替换：`"You are an AI-powered code editor."`
- **Copilot 结对编程身份**：
  - 匹配：`"You are an AI pair programmer."`
  - 替换：`"You are an AI pair programmer."`

---

## 编译与部署

### 1. 编译
```bash
cd stealth-plugin
CGO_ENABLED=1 go build -buildmode=c-shared -o stealth.dylib .
```
*(Windows 下生成 `.dll`，Linux 下生成 `.so`)*

### 2. 部署到网关
将编译产物拷贝至网关的 `plugins/` 目录：
```bash
cp stealth.dylib ~/Projects/Gemini\ API/plugins/
```

### 3. 在 `config.yaml` 中启用
```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    stealth:
      enabled: true
      priority: 200
```

### 4. 重启网关
重启 `cli-proxy-api` 即可立即对所有经过网关的流量生效。
