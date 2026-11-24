<div align="center">

<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/lockup-horizontal-dark.svg">
    <img src="./assets/lockup-horizontal.svg" alt="Kettu" width="300">
  </picture>
  <br>
  Relay
</h1>

[![CI](https://img.shields.io/github/actions/workflow/status/KettuAI/relay/ci.yml?branch=main&label=CI&style=flat-square&labelColor=3f3f46)](https://github.com/KettuAI/relay/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/KettuAI/relay?label=go&style=flat-square&labelColor=3f3f46&color=0f766e)](./go.mod)
[![许可证](https://img.shields.io/github/license/KettuAI/relay?label=%E8%AE%B8%E5%8F%AF%E8%AF%81&style=flat-square&labelColor=3f3f46&color=71717a)](./LICENSE)

<h3>Kettu 的自建中继。</h3>

[**下载**](https://github.com/KettuAI/relay/releases) · [Kettu](https://github.com/KettuAI/Kettu) · [部署指南](./docs/deploy.zh-CN.md) · [协议](./PROTOCOL.md) · [English](./README.md)

</div>

kettu-relay 是 [Kettu](https://github.com/KettuAI/Kettu) 的中继，运行在你自己的服务器上。运行 Kettu 的电脑用它的访问密钥登录上来，你的手机、浏览器或别的电脑就能在任何网络下经过它连上这些电脑，不需要 Kettu 账号。

它只原样转发 Kettu 端到端加密的通道，看不到你的会话、文件和密钥。它就是一个小程序，或者一个 Docker 镜像。

## 快速开始

把域名解析到你的服务器，放开 80 和 443 端口，然后装成系统服务（Linux，systemd）：

```sh
curl -fsSL https://raw.githubusercontent.com/KettuAI/relay/main/install.sh | sudo sh -s -- --domain relay.example.com
```

或者用 Docker 运行：

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 80:80 -p 443:443 \
  -e KETTU_RELAY_DOMAIN=relay.example.com \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
docker exec kettu-relay kettu-relay link
```

把打印出的链接粘贴到 Kettu 的 **设置 › 远程访问 › 中继 › 自己的中继** 里。放在自己的反向代理后面、只有 IP 地址，以及全部设置，见[部署指南](./docs/deploy.zh-CN.md)。

## 许可证

[AGPL-3.0](./LICENSE)。安全问题请按 [SECURITY.md](./SECURITY.md) 报告。
