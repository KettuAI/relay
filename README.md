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
[![License](https://img.shields.io/github/license/KettuAI/relay?style=flat-square&labelColor=3f3f46&color=71717a)](./LICENSE)

<h3>Your own relay for Kettu.</h3>

[**Releases**](https://github.com/KettuAI/relay/releases) · [Kettu](https://github.com/KettuAI/Kettu) · [Deployment guide](./docs/deploy.md) · [Protocol](./PROTOCOL.md) · [简体中文](./README.zh-CN.md)

</div>

kettu-relay is a relay for [Kettu](https://github.com/KettuAI/Kettu) that you run on your own server. The computers that run Kettu sign in to it with its access key, and your phone, browser or other computers reach them through it from any network, without a Kettu account.

It only passes Kettu's end-to-end encrypted channel along, so it never sees your sessions, files or keys. It is one small program, or one Docker image.

## Quick start

Point a domain at your server, open ports 80 and 443, and install it as a service (Linux with systemd):

```sh
curl -fsSL https://kettu.sh/relay/install | sudo sh -s -- --domain relay.example.com
```

Or run it in Docker:

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 80:80 -p 443:443 \
  -e KETTU_RELAY_DOMAIN=relay.example.com \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
docker exec kettu-relay kettu-relay link
```

Paste the printed link into Kettu under **Settings › Remote access › Relay › Your own relay**. Running it behind your own proxy or on an IP address alone, and every setting, are in the [deployment guide](./docs/deploy.md).

## License

[AGPL-3.0](./LICENSE). Report security problems as described in [SECURITY.md](./SECURITY.md).
