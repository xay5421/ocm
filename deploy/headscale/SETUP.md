# Headscale 部署手册

自建 Tailscale 协调服务器，让手机、PC、各远程开发机组成一个私有 tailnet。
所有客户端用官方 Tailscale app/包，只是登录时指向你自己的服务器。

## 0. 前提

- 一台公网服务器（已装 docker + docker compose）
- 一个域名，例如 `hs.example.com`，DNS A 记录指向该服务器
- 云安全组放行：`80/tcp`（证书签发）、`443/tcp`（协调 + DERP 中继）、`3478/udp`（STUN 打洞）

## 1. 部署服务端

把本目录（`docker-compose.yml`、`Caddyfile`、`config/config.yaml`）传到服务器，
把两处 `hs.example.com` 改成你的域名：

- `Caddyfile` 第一行
- `config/config.yaml` 的 `server_url`

然后：

```bash
docker compose up -d
docker compose logs -f headscale   # 确认无报错后 Ctrl-C
```

验证：浏览器打开 `https://你的域名/health`，应返回 JSON。

## 2. 创建用户和预授权密钥

```bash
docker exec headscale headscale users create me
# 给每台要入网的机器生成一个 key（--reusable 可多台复用，24h 内有效）
docker exec headscale headscale preauthkeys create --user me --expiration 24h --reusable
```

## 3. 各端入网

### Linux 开发机（每台）

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --login-server=https://你的域名 --authkey=<预授权key> --accept-dns=false
```

`--accept-dns=false`：不接管开发机的 DNS，避免影响公司内网解析；用 IP 直连即可。

### Android 手机

1. 安装官方 Tailscale app
2. 首次登录前：设置（右上角头像/齿轮）→ Accounts → 右上角菜单 →
   **Use an alternate server**，填 `https://你的域名`
3. 按提示走浏览器登录，页面会显示一条命令，回服务器执行：

```bash
docker exec headscale headscale nodes register --user me --key <页面上的 mkey>
```

### Windows PC（可选，PC 也入网的话 ocm 以后可以走 tailnet）

```powershell
tailscale login --login-server https://你的域名
```

## 4. 验证

```bash
docker exec headscale headscale nodes list
```

应看到所有设备和各自的 `100.64.x.x` 地址。在手机上 ping 某台开发机的
tailnet IP，或用任意 ssh app 连 `100.64.x.x` 验证连通。

打洞失败时流量自动走你服务器的 DERP 中继（443），有保底连通性。

## 5. 日常运维

```bash
docker compose pull && docker compose up -d   # 升级
docker exec headscale headscale nodes list     # 查看设备
docker exec headscale headscale nodes expire -i <id>   # 踢设备
# 数据都在 ./data 和 ./config，备份这两个目录即可
```
