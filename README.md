# okboy

**基于 nftables 的动态防火墙白名单管理器** — 授权用户认证后自动把其 IP 注册进 nftables，IP 变化时无缝切换，规则整洁可追溯。Go 实现，**单个静态二进制**，无运行时依赖。

[English](README.en.md) | 简体中文

> 这是 [ufw-okboy](https://github.com/lvusyy/UFW-OkBoy)（Python + UFW）的 Go + nftables 重写：忠实保留全部认证/授权/安全语义，底层换成 nftables 原生、交付物换成单静态二进制。

---

## 架构

```
客户端（浏览器 / Python / Shell）
    │  HTTPS + HMAC-SHA256 签名
    ▼
Nginx（TLS 终止，传 X-Real-IP）
    │  HTTP 127.0.0.1:5000
    ▼
okboy（Go：HTTP API + CLI + 鉴权 + 限流）
    │  nft -j -f -（JSON 事务，无 shell）
    ▼
nftables（专用 inet okboy 表，accept-only，与 k8s/host 规则共存）
    │
    ▼
SQLite（modernc 纯 Go；users/groups/membership/audit）
```

## 为什么

- **单静态二进制**：`CGO_ENABLED=0` + 纯 Go SQLite（modernc）→ 一个文件丢到任意 Linux 即跑，无 Python/venv/gunicorn/libc 依赖。
- **nftables 原生**：现代发行版默认 nftables；okboy 走 `nft` 的 JSON 接口，规则带 `okboy:<user>:<group>` 注释、按 handle 精确增删、单事务原子 reconcile。
- **与 k8s 共存**：okboy 独占 `inet okboy` 表，hook input、priority -150、**policy accept、只加 accept 规则**——只能为指定 IP/端口"放行"，永不 drop，也不会被 Calico/Cilium/kube-proxy flush 影响（表隔离）。

## 安全特性（与 Python 版对等 + 强化）

| 特性 | 说明 |
|------|------|
| **HMAC-SHA256 无状态认证** | 密钥不上线；时间戳窗口；失败全部记录 |
| **管理员 TOTP step-up** | 所有管理写操作启用 TOTP 后需 6 位动态码（RFC 6238）|
| **TOTP 重放保护** | 用过的码在窗口内重放即拒（last-counter，RFC 6238 §5.2）|
| **按 IP 限流** | 同 IP 失败过多 → 429（按 IP 不按用户名，防锁定 DoS）|
| **名称白名单 (SR-1)** | 用户名/组名 `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`，杜绝 nft 注入 |
| **防 IP 伪造 (H-9)** | X-Forwarded-For 取**最右** hop，仅信任 `trusted_proxies` |
| **下线 + 强制重认证** | `revoke`：关端口 + 清状态 + 轮换密钥（旧凭据即失效）|
| **注入安全** | 写操作走 `nft -j -f -` STDIN（encoding/json 转义，无 shell，无 argv 注入）|

## 快速开始

**构建单静态二进制：**

```bash
make static          # → dist/okboy-linux-amd64（CGO_ENABLED=0，静态）
```

**配置 + 运行：**

```bash
cp config.example.yaml config.yaml
./okboy gen-secret alice                 # 生成用户密钥
./okboy -c config.yaml user-add alice    # 建用户（或编辑 config.yaml 的 users:）
./okboy -c config.yaml group-add ssh 22  # 建组（绑端口 22）
./okboy -c config.yaml user-join alice ssh
sudo ./okboy -c config.yaml serve        # 启动（需 root 或 CAP_NET_ADMIN）
```

**客户端**：浏览器打开 `https://your-server/` → 输入用户名+密钥 → Connect。或复用 ufw-okboy 的 `knock.py` / `knock.sh`（API 契约一致）。

## 测试

```bash
make test          # 单元测试（任意平台：auth RFC 向量 + firewall Mock reconcile）
make integration   # 真实 nftables 集成测试（Linux+root，跑在隔离 netns，零 host/k8s 影响）
```

`internal/firewall/mock.go` 让核心 reconcile/鉴权逻辑可在非 Linux 开发机单测；`nft_integration_test.go`（`-tags integration`）在隔离 network namespace 内对**真实 nft** 验证 EnsureBase 幂等 / 增规则 / handle 列出 / 同端口跨组精确删 / IPv6 / ListManaged。

## 部署

```bash
install -Dm755 dist/okboy-linux-amd64 /opt/okboy/okboy
install -Dm600 config.yaml /etc/okboy/config.yaml
install -Dm644 deploy/okboy.service /etc/systemd/system/okboy.service
systemctl enable --now okboy
# nginx：见 deploy/nginx-okboy.conf（务必 proxy_set_header X-Real-IP $remote_addr）
```

## CLI 命令

```
serve [--debug]          启动 API 服务
gen-secret [user]        生成密钥
user-add <name> [--admin] / user-del / user-list
group-add <name> <port> [--proto tcp] / group-del / group-list
user-join <user> <group> / user-leave <user> <group>
admin-add <user>         授予管理员
revoke <user> [--no-rotate]   下线 + 轮换密钥
list                     列出受管 nftables 规则
cleanup [--max-age <days>]    清理过期规则
backup [--dir <path>]    校验和在线备份
--version
```

## 目录结构

```
cmd/okboy/            main：子命令分发
internal/config/      YAML 配置加载
internal/db/          SQLite 层（schema + 迁移 + CRUD + 原子 IP 写 + 备份）
internal/auth/        HMAC 验签 + TOTP + 按 IP 限流
internal/firewall/    FirewallBackend 接口 + nftables 实现 + Mock + Manager(reconcile)
internal/server/      stdlib net/http 路由（mirror app.py 全部端点）
internal/static/      go:embed 的单文件 Web UI
deploy/               systemd unit + nginx 示例
```

## 许可证

MIT
