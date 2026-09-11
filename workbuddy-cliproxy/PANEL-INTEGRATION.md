# 面板集成（Control Panel Integration）

WorkBuddy 的签到与积分**通过插件原生页面**集成进 CLIProxyAPI 面板 —— **不需要修改面板**，
因此面板的自动更新可以保持开启。

## 一、原生机制（推荐方案）

面板原生支持插件资源页面：

1. 插件在 `management.register` 里注册一个 **GET 资源路由**，并带上 `Menu` 标签
2. 宿主把它下发到 `GET /v0/management/plugins` 的 `menus` 字段
3. 面板侧边栏原生渲染一个导航组 **「插件」**（`nav_groups.plugin_pages`），
   每个菜单项对应路由 `/plugin-pages/<插件id>/<序号>`
4. 该路由用 **iframe** 嵌入插件页面

证据（面板压缩包内）：

```js
NM = plugins.flatMap(p => p.menus.map((m, i) => ({
  pluginID: p.id, menuIndex: i, label: m.menu,
  route: '/plugin-pages/' + p.id + '/' + i
})))
// 侧边栏：pluginPages.length > 0 ? [{ labelKey: 'nav_groups.plugin_pages', items }] : []
```

所以 WorkBuddy 的账号面板就在面板侧边栏的**「插件」**分组下，无需任何补丁。

## 二、插件页面拿到管理密钥的方式

资源路由是**免鉴权**的（宿主侧 `/v0/resource/plugins/` 不走管理中间件），
而面板把密钥存在**加密存储**里（`enc::v1::` 前缀，密钥由 host + userAgent 派生），
所以页面不能直接读。

**做法**：iframe 与面板**同源**，页面在加载时包装**父窗口**的
`XMLHttpRequest.prototype.setRequestHeader` 与 `fetch`，从面板自己发出的管理请求里
取 `Authorization` 头。密钥始终留在浏览器内。

> ⚠️ **绝不猜测密钥**。CPA 管理鉴权是 `maxFailures = 5`、封禁 30 分钟。
> 页面在拿到密钥前**一个请求都不发**；遇到 401/403 立即丢弃并等待重新嗅探。
> 无头验证里有一条安全断言守住这个行为。

独立打开页面（不在 iframe 里）时，会提示手动粘贴一次；粘贴的密钥写入
`workbuddy-console-key` / `workbuddy-panel-key` 槽位。

## 三、刷新凭证

面板自带的刷新按钮可见性是**硬编码白名单**：

```js
AO = new Set(['antigravity','claude','codex','kimi','xai'])
w  = !x && PO(card.provider)
```

workbuddy 不在其中，所以插件页面自己渲染刷新按钮。

**刷新按钮由插件自己完成，不走面板的过期标记机制。** 原因在宿主源码里：

```go
// sdk/cliproxy/auth/auto_refresh_loop.go
func nextRefreshCheckAt(now time.Time, auth *Auth, interval time.Duration) (time.Time, bool) {
    ...
    if pref := authPreferredInterval(auth); pref > 0 { ... }   // ① metadata/attributes
    lead := ProviderRefreshLead(provider, auth.Runtime)        // ② 宿主内注册表
    if lead == nil { return time.Time{}, false }               // → 永不进入刷新调度
}
```

- ② 的注册表在宿主进程内，`sdk/auth/refresh_registry.go` 的 `init()` 只注册了
  codex / claude / antigravity / kimi / xai——**动态插件无法往宿主的表里加项**
  （它能调到的只是自己那份 SDK 副本）。面板白名单正是这 5 个 provider。
- ① 是插件唯一能影响的开关：`toAuthData()` 往 metadata 里发布
  `refresh_interval_seconds`（7 天）、`expired`（真实到期时间，来自
  `auth.expiresAt`）、`last_refresh`（token 签发时间，来自 `auth.issuedAt`）。
  没有前两项宿主根本不给这张凭证排期；少了 `last_refresh`，宿主会把每次启动都
  当成「从未刷新过」，于是**每次重启都多轮换一次 token**。

即使排期成立，「标记过期后等宿主来刷」仍然有两处不可靠：

1. PATCH 会把 `expired` 写进凭证文件，文件监听器随即重载并调用插件的
   `auth.parse`，把 metadata 换成插件发布的真实到期时间——**手动标记可能在下一次
   循环 tick（5 秒）之前就被覆盖**；
2. 反馈延迟不可控，按钮只能报「已请求刷新」，无法告知结果。

所以刷新动作放在插件里：`POST /v0/management/plugins/workbuddy/refresh`

```
{ "auth_index": "<idx>" }
→ { "ok": true, "name": "workbuddy.json",
    "expires_at": "2026-11-10T12:44:11Z",
    "message": "凭证已刷新，有效期至 2026-11-10 20:44" }
```

插件调用 `POST /v2/plugin/auth/token/refresh` 轮换 token，再用 `host.auth.save`
写回凭证文件。写回采用**合并**而非重新编码：插件只建模 `auth` / `account`
两段，直接序列化自己的结构体会丢掉 `type`，而 `type` 一旦丢失宿主就不再把这
个文件认作本 provider。

两条路径并存且不冲突：插件发布的 `refresh_interval_seconds` 让宿主每 7 天自动
轮换一次（access token 寿命约 60 天，留足余量），按钮提供随时的手动轮换。

## 四、插件侧配套路由

| 方法 | 路径 | 成本 | 用途 |
|------|------|------|------|
| GET | `/v0/management/plugins/workbuddy/accounts` | **零上游调用** | 账号列表（画卡片框用） |
| GET | `/v0/management/plugins/workbuddy/accounts/status` | 每账号 2 次上游 | 额度 + 签到态（带 `auth_index` 则只查该账号） |
| GET | `/v0/management/plugins/workbuddy/checkin/status` | 1 次上游 | 定向查签到状态 |
| POST | `/v0/management/plugins/workbuddy/checkin/claim` | 1–2 次上游 | 定向签到 |
| GET | `/v0/management/plugins/workbuddy/quota` | 1 次上游 | 定向查额度 |
| POST | `/v0/management/plugins/workbuddy/refresh` | 1 次上游 | 定向轮换凭证并写回 |
| GET | `/v0/resource/plugins/workbuddy/checkin` | 无 | **原生页面**（免鉴权，面板 iframe 嵌入） |

`auth_index` 可放在 query string 或 JSON body。

### 为什么拆成快/慢两条

额度查询每个账号要 2 次上游往返，无法避免。所以**画框**用零成本的 `/accounts`，
**取额度**用按需的 `/accounts/status`：

- 页面加载后卡片框**立即出现**，内容为「点击此处刷新额度」
- 只有操作者点击才真正请求，交互与面板自带额度卡片一致

多账号时 `/accounts/status` 内部按账号**并发**（每次请求的两次上游调用也并发）。

## 五、验证

| 测试 | 内容 |
|------|------|
| `scripts/verify-console-page.mjs` | 无头跑插件页面：父窗口嗅探、不猜测密钥、渲染卡片、定向签到、刷新请求（17 项断言） |
| `TestConsolePageBrowserLogic` | 把上述脚本纳入 Go 测试套件 |
| `scripts/verify-panel-script.mjs` | （可选注入方案）22 项断言 |
| `scripts/inject-panel.mjs` | （可选）向面板追加脚本；默认不使用 |

## 六、配额管理页：为什么必须注入 + 如何兼得更新

面板「配额管理」页把 provider **硬编码死了**：

```js
CA = { antigravity:{...}, claude:{...}, codex:{...}, kimi:{...}, xai:{...} }
OA = e => { throw Error('Unsupported quota type: ' + e) }   // 未知类型直接抛错
```

workbuddy **不可能**在该页原生出现。想让配额卡片出现在那一页，只能注入。

**代价原本是**必须关闭面板自动更新（否则 CPA 启动时用官方构建覆盖注入）。

### 解法：自动重放的守护进程

保留 `disable-auto-update-panel: false`（面板照常更新），另装一个 LaunchAgent
在面板被更新后**自动重新注入**：

```bash
bash scripts/install-panel-inject-agent.sh
```

- 登录时运行一次，之后每 60 秒检查一次
- 已注入且是最新版 → 立即退出（稳态开销：每分钟一次 node 启动，几毫秒）
- 检测到注入缺失/过期 → 自动重新注入，日志写入
  `~/.cli-proxy-api/logs/workbuddy-panel-inject.log`
- 脚本与注入内容会**复制到内盘** `~/Library/Application Support/workbuddy-panel/`
  （launchd 无法执行外置硬盘上的脚本）

于是：**配额卡片 + 面板可持续更新，两者兼得。**

## 七、可选：把按钮叠加到面板自己的卡片上

如果希望在面板「认证文件」页的卡片上直接出现签到/刷新按钮，可以用
`scripts/inject-panel.mjs` 追加注入脚本（不改压缩代码）。**代价**是必须关闭面板自动更新，
否则 CPA 启动时会用官方构建覆盖注入：

```yaml
remote-management:
  disable-auto-update-panel: true
```

升级面板时：改回 `false` → 重启 → 重新运行注入脚本。

> 这是**非必需**的增强。默认方案（原生插件页面）已经覆盖全部功能，且不影响 CPA 更新。
