# bin/ —— CPA 本体（不随仓库分发）

这个目录**只放说明，不放二进制**。

真正的 `bin/cli-proxy-api` 是 CLIProxyAPI 本体，约 **57MB**，提交进 git 会让每次克隆都多拉这么多，而且二进制无法增量存储——以后升级 CPA 会在历史里再压一份等大的。用下面的脚本按需拉取即可。

## 怎么拿到它

```bash
cd "$(git rev-parse --show-toplevel)"   # 仓库根目录
CLIPROXY_VERSION=v7.2.149 bash scripts/download.sh
```

脚本会：

1. 按 `uname -m` 选对架构（`arm64` → `aarch64`，`x86_64` → `amd64`）
2. 从 GitHub Releases 下载 `CLIProxyAPI_7.2.149_darwin_<arch>.tar.gz`
3. 装到 `bin/cli-proxy-api`（权限 `0755`），并清掉 macOS 的隔离属性
4. **校验版本**：跑 `cli-proxy-api -h`，第一行必须逐字等于

   ```
   CLIProxyAPI Version: 7.2.149, Commit: 2a6b87ac, BuiltAt: 2026-09-03T13:31:22Z
   ```

   对不上就删掉并报错退出。

## 版本是钉死的

`scripts/download.sh` 里写死了 `v7.2.149` / `2a6b87ac`。**本仓库的插件（WorkBuddy 0.3.2）只在 7.2.149 上验证过**，登录流程、凭据文件格式、管理面板都可能依赖这一版的行为。

想装别的版本会被拒绝。确实要试，先读脚本头部的警告，然后：

```bash
ALLOW_UNPINNED_CPA=1 CLIPROXY_VERSION=v7.x.y bash scripts/download.sh
```

试完记得把插件重新测一遍——尤其是扫码登录和凭证刷新。

## 为什么不提交

| 理由 | 说明 |
|------|------|
| 体积 | 57MB × 每次克隆；CPA 一升级历史里再添一份 |
| 可复现 | 版本、commit、构建时间都在脚本里钉死，`download.sh` 能一字不差地重建 |
| 架构差异 | 本机是 darwin/arm64，换 Intel 机器或 Linux 需要另一份二进制 |
| 许可证与来源 | 直接从上游 Releases 取，仓库不承担再分发 |

## 相关

- 编译 WorkBuddy 插件：`workbuddy-cliproxy/scripts/install-local.sh`
- 装成后台服务：`scripts/install-service.sh`（plist 在 `scripts/local.cliproxyapi.plist`）
