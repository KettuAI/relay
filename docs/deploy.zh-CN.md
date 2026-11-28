# 运行 kettu-relay

怎么在你自己的服务器上运行中继、让 Kettu 连上它，以及全部设置。[English](./deploy.md)

## 运行

把域名解析到你的服务器，放开 80 和 443 端口，然后运行：

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 80:80 -p 443:443 \
  -e KETTU_RELAY_DOMAIN=relay.example.com \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
```

中继会自己从 Let's Encrypt 申请和续期证书。镜像（amd64 和 Arm）以非特权用户运行，只写 `/data`。80 端口只用于 Let's Encrypt 的验证和跳转到 https；设 `KETTU_RELAY_HTTP_LISTEN=off` 后，中继只在 443 上应答验证。

然后打印中继的链接，里面有它的地址和访问密钥：

```sh
docker exec kettu-relay kettu-relay link
```

```
https://relay.example.com#key=krk_...
```

> [!IMPORTANT]
> 拿到这个链接的人都能把电脑登录到你的中继上。请自己保管好；如果它出现在你管不了的日志或聊天里，运行 `kettu-relay key reset`，再把新链接粘贴到每台电脑上。

第一次启动会完整打印一次链接；之后的启动只记下地址，密钥打码（`krk_…3hT9`）。在第一次启动前先运行 `kettu-relay link`，密钥就完全不会进日志。

## 其他运行方式

### 放在你自己的反向代理后面

如果服务器上已经有 Caddy、nginx 或隧道在提供 https，就让中继跑在本机端口上，并告诉它别人访问用的地址：

```sh
KETTU_RELAY_URL=https://relay.example.com kettu-relay
```

它监听 `127.0.0.1:8798`（用 `KETTU_RELAY_LISTEN` 修改）。用 Caddy：

```
relay.example.com {
	reverse_proxy 127.0.0.1:8798
}
```

nginx 需要转发 WebSocket 升级：

```nginx
location / {
    proxy_pass http://127.0.0.1:8798;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 1h;
}
```

只有请求来自受信任的代理时，中继才从 `X-Forwarded-For` 取客户端地址：默认是回环地址，或 `KETTU_RELAY_TRUSTED_PROXIES` 里的地址和 CIDR 网段。在代理后面时，中继也只接受来自这些地址的连接，其余一律拒绝，因为它自己的端口提供的是明文 http。

在 Docker 里，把 `KETTU_RELAY_TRUSTED_PROXIES` 设成代理所在的网络，设 `KETTU_RELAY_LISTEN=:8798`，并且不要对外发布这个端口，只让代理能访问。中继只要监听的不是回环地址，就会在日志里提醒这一点。


### 只有 IP 地址

没有域名时，中继会自己生成证书，并打印它的 pin，也就是公钥的 SHA-256，这个 pin 会写进链接：

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 443:443 \
  -e KETTU_RELAY_IP=203.0.113.7 \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
```

链接会是 `https://203.0.113.7#key=krk_...&pin=...`，Kettu 凭这个 pin 信任中继，而不是凭证书颁发机构。桌面应用、daemon 和 iPhone 应用都能用这样的中继。浏览器没法固定证书，所以浏览器需要有域名的中继；在 IP 地址上，中继也不提供网页版。


### 用 systemd

在 Linux 上，安装脚本会一次做完：从 Kettu 的下载服务下载最新版本（失败时改从 GitHub 的 [releases](https://github.com/KettuAI/relay/releases) 下载）、用 `SHA256SUMS` 校验、创建 `kettu-relay` 用户、安装程序和 [`deploy/kettu-relay.service`](../deploy/kettu-relay.service) 里的 unit、把设置写进 `/etc/kettu-relay.env`、启动服务并打印链接。`--domain`、`--ip` 和 `--url` 给一个：

```sh
curl -fsSL https://kettu.sh/relay/install | sudo sh -s -- --domain relay.example.com
```

再运行一次就是更新程序，设置和访问密钥都保留。`--version X.Y.Z` 安装指定版本；加 `--no-service`（或在 macOS 上运行）只安装程序。

想手动装，就从 [releases](https://github.com/KettuAI/relay/releases) 下载对应系统的压缩包，用 `SHA256SUMS` 校验后解压，按 unit 文件开头写的步骤做。每个压缩包里都有程序、它的许可证和 `THIRD_PARTY_NOTICES.txt`。

以中继的用户身份运行它的命令，这样读到的是同一份文件：

```sh
sudo -u kettu-relay sh -c 'set -a; . /etc/kettu-relay.env; kettu-relay link'
```


### 在一台电脑上试试

`KETTU_RELAY_DOMAIN`、`KETTU_RELAY_URL` 和 `KETTU_RELAY_IP` 都不设时，中继只在 `127.0.0.1:8798` 上提供明文 http，用来和同一台电脑上的 Kettu 试用：

```sh
go run github.com/KettuAI/relay/cmd/kettu-relay@latest
```

明文 http 永远不会在别的地址上提供，因为访问密钥要经过网络。


## 连上 Kettu

- **每台电脑**：在 Kettu 里打开 **设置 › 远程访问 › 中继**，选 **自己的中继**，粘贴链接，再点 **使用这个中继**。Kettu 先检查中继和密钥，过了才切换。
- **运行 Kettu daemon 的服务器**：运行 `kettu relay use '<链接>'`，或者把 `KETTU_RELAY_LINK` 设成这个链接，每次启动都会检查并使用它。`kettu relay status` 显示正在用的中继。
- **你的设备**：照常用 **添加设备** 添加；配对码里带着你的中继，但从不带它的密钥。切换时在线的设备会马上跟过去，而且不需要 Kettu 账号。

一台电脑一次只用一个中继。想换回去，在同一个地方选 **Kettu 中继** 或 **不用中继**。

## 命令

| 命令 | 作用 |
| --- | --- |
| `kettu-relay` | 运行中继。第一次启动打印它的链接；之后的启动只记下打码后的链接。 |
| `kettu-relay link` | 打印完整链接；还没有访问密钥时会先生成。 |
| `kettu-relay key reset` | 生成新的访问密钥。正在运行的中继会在几秒内断开用旧密钥登录的电脑；把新链接粘贴到每台电脑上。 |
| `kettu-relay web update` | 立即检查并更新网页版：中继正在运行时原地热切换而不重启或断开连接；中继未运行时直接更新磁盘上的副本。 |
| `kettu-relay version` | 打印版本。 |

在 Docker 里，在容器中运行：`docker exec kettu-relay kettu-relay link`。

## 设置

所有设置都是环境变量。前三个决定中继怎么拿到证书，最多只设一个。

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `KETTU_RELAY_DOMAIN` | | 为这个域名提供 https，证书来自 Let's Encrypt。 |
| `KETTU_RELAY_URL` | | 在你的代理后面提供明文 http；这是别人访问用的 https 地址。 |
| `KETTU_RELAY_IP` | | 在这个 IP 地址上提供 https，用自签名证书。 |
| `KETTU_RELAY_LISTEN` | `:443`；在代理后面或只在一台电脑上时为 `127.0.0.1:8798` | 监听的地址。有域名或 IP 时，443 以外的端口会成为中继地址的一部分。 |
| `KETTU_RELAY_HTTP_LISTEN` | `:80` | 有域名时：Let's Encrypt 的 http 验证和跳转到 https。`off` 表示不用。 |
| `KETTU_RELAY_DATA` | Docker 里是 `/data`，否则是用户的配置目录 | 存放密钥、证书和网页版的位置。 |
| `KETTU_RELAY_ACCESS_KEY` | | 访问密钥，代替密钥文件。它从不完整打印，`kettu-relay key reset` 也改不了它。 |
| `KETTU_RELAY_ACME_EMAIL` | | Let's Encrypt 可以就证书联系你的邮箱。 |
| `KETTU_RELAY_TRUSTED_PROXIES` | 在代理后面时为回环地址，否则为空 | 哪些地址和 CIDR 网段的 `X-Forwarded-For` 可信。 |
| `KETTU_RELAY_WEB` | `on` | `off` 表示不提供网页版。 |
| `KETTU_RELAY_CONNECTIONS_PER_MINUTE` | `120` | 同一地址每分钟的新连接数。 |
| `KETTU_RELAY_DEVICES_PER_MINUTE` | `60` | 同一地址每分钟连向同一台电脑的新设备连接数。 |
| `KETTU_RELAY_MAX_CONNECTIONS` | `4096` | 中继同时保持的连接数；超过后新连接返回 503。 |
| `KETTU_RELAY_MAX_CONNECTIONS_PER_ADDRESS` | `64` | 同一地址同时保持的连接数；超过后新连接返回 429。 |
| `KETTU_RELAY_MAX_PENDING_DEVICES` | `16` | 同一台电脑上等待连接的设备数；超过后等得最久的那个被断开。 |
| `KETTU_RELAY_JOIN_TIMEOUT` | `10` | 设备连上后等电脑应答的秒数，超时两端都会被关闭。 |

按协议规定，每台电脑同时最多连 16 台设备，一条消息最大 64 KiB，电脑或设备有 10 秒时间完成登录或连接。设备不带密钥，所以正是这些限制，让知道某台电脑 id 的陌生人占不满它的名额、拖不垮中继：从不配对的连接不会挤掉电脑真正设备的名额。


## 中继能看到什么

- **能看到的**：连接来自哪个地址、电脑的中继 id（由电脑的公钥推出），以及它转发的加密消息的大小和时间。
- **看不到的**：消息里的内容。通道在电脑和设备之间加密，电脑自己决定和哪些设备说话。
- **会保存的**：不在磁盘上记录任何连接，日志里也不留 id 和地址。磁盘上只有它的访问密钥、证书（连同 Let's Encrypt 账号）和一份网页版，都放在权限为 0700 的数据目录里。密钥文件或私钥能被其他用户读取，或者数据目录属于别的用户时，中继拒绝启动。

## 网页版

为了让浏览器也能经你的中继连上你的电脑，中继在自己的地址上提供 Kettu 的网页版。它在启动时和之后每天，从 GitHub 上最新的 [Kettu 发布](https://github.com/KettuAI/Kettu/releases)下载为中继构建的网页版（连不上 GitHub 时改从 Kettu 的下载服务下载），用发布里的 SHA-256 校验和检查后解压到数据目录。运行 `kettu-relay web update`（或在 Docker 中执行 `docker exec kettu-relay kettu-relay web update`）即可立即检查并热切换，无需重启中继。下载失败时，它继续提供已有的那份；一次都没下载成功之前，它不提供网页版，并在日志里说明。

这样一来，电脑上的 **添加设备** 会给浏览器一个 `https://<你的中继>/pair#...` 链接。这个页面只和它自己的来源，也就是中继通信，配对和使用会话都和托管的网页版一样。

运行中继的人可以改它提供的页面，所以经中继打开网页版的浏览器信任的是中继的运营者；在你自己的中继上，运营者就是你。桌面应用和 iPhone 应用都不依赖它。

## 版本

kettu-relay 遵循[语义化版本](https://semver.org/lang/zh-CN/)：修复是补丁版本，新功能是次版本，去掉任何东西是主版本。它提供中继协议 1，写在 [PROTOCOL.md](../PROTOCOL.md) 里，这个协议只增不减。每个版本的变化见 [CHANGELOG.md](../CHANGELOG.md)。

## 从源码构建

```sh
go build ./cmd/kettu-relay
go test -race ./...
```

中继用的是 Go 标准库、用于 Let's Encrypt 的 `golang.org/x/crypto`，以及 `github.com/coder/websocket`。它们的许可证在 [`THIRD_PARTY_NOTICES.txt`](../THIRD_PARTY_NOTICES.txt) 里，由 `scripts/notices.sh` 生成。`github.com/KettuAI/relay/relay` 这个包就是中继本身，谁能登录交给一个 `Admission` 决定，所以别的程序可以在它上面构建。
