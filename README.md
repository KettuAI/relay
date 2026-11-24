# kettu-relay

A relay for [Kettu](https://github.com/KettuAI/Kettu) that you run on your own server. Your Kettu
computers sign in to it, and your phone, browser or other computers reach them through it from any
network, without a Kettu account.

kettu-relay is one small program with no runtime to install. It joins a device to a computer and
then passes their messages unchanged. Those messages are Kettu's end-to-end encrypted channel, so
the relay never sees your sessions, files or keys.

## What the relay sees, and what it does not

- It sees the address a connection comes from, a computer's relay id (derived from the computer's
  public key) and the size and timing of the encrypted messages it passes.
- It does not see what is in them: the channel is encrypted between the computer and the device,
  and the computer decides which devices it talks to.
- It writes nothing about connections to disk and keeps no ids or addresses in its log. On disk it
  keeps only its access key, its certificates (and the Let's Encrypt account), and a copy of the
  web client.

Only computers that have the relay's **access key** can sign in. Devices bring no key: they ask for
a computer by its id, and the computer checks them inside the encrypted channel, exactly as through
Kettu's own relay. The key stays on your computers and is never put in a pairing code, so losing a
phone never means changing the key.

## Quick start

Each start prints a link with the relay's address and access key:

```
Paste this link into Kettu, in Settings > Remote access > Your own relay:

  https://relay.example.com#key=krk_...
```

Paste it into Kettu on each computer, under **Settings › Remote access › Your own relay**, and
choose **Use this relay**. Print the link again any time with `kettu-relay link`.

Pick one of the ways below to give the relay a certificate.

### Docker, with a domain

Point a domain at your server, open ports 80 and 443, and run:

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 80:80 -p 443:443 \
  -e KETTU_RELAY_DOMAIN=relay.example.com \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
docker logs kettu-relay
```

The relay gets and renews its certificate from Let's Encrypt by itself. The image runs as an
unprivileged user and writes only to `/data`. Port 80 is only for Let's Encrypt's challenge and the
redirect to https; with `KETTU_RELAY_HTTP_LISTEN=off` the relay answers the challenge on 443 alone.

### Behind your own proxy

If Caddy, nginx or a tunnel already serves https on the server, run the relay on a local port and
tell it the address people use:

```sh
KETTU_RELAY_URL=https://relay.example.com kettu-relay
```

It listens on `127.0.0.1:8798` (change it with `KETTU_RELAY_LISTEN`). With Caddy:

```
relay.example.com {
	reverse_proxy 127.0.0.1:8798
}
```

nginx needs WebSocket upgrades passed on:

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

The relay takes the client's address from `X-Forwarded-For` only when the request comes from a
trusted proxy: by default loopback addresses, or the addresses and CIDR ranges in
`KETTU_RELAY_TRUSTED_PROXIES`. In Docker, set it to your proxy's network and
`KETTU_RELAY_LISTEN=:8798`.

### On an IP address alone

Without a domain, the relay makes its own certificate and prints its pin, the SHA-256 of its public
key, which goes into the link:

```sh
docker run -d --name kettu-relay --restart unless-stopped \
  -p 443:443 \
  -e KETTU_RELAY_IP=203.0.113.7 \
  -v kettu-relay:/data \
  ghcr.io/kettuai/relay:latest
```

The link then reads `https://203.0.113.7#key=krk_...&pin=...`, and Kettu trusts the relay by that
pin instead of a certificate authority. The desktop app, the daemon and the iPhone app can use such
a relay; a browser cannot pin a certificate, so it needs a relay with a domain, and the relay
serves no web client on an IP address.

### systemd

Download the binary for your system from the
[releases](https://github.com/KettuAI/relay/releases), check it against `SHA256SUMS`, and use the
unit in [`deploy/kettu-relay.service`](deploy/kettu-relay.service), whose header lists the steps.
Run the commands below as the relay's user so they read the same files:

```sh
sudo -u kettu-relay sh -c 'set -a; . /etc/kettu-relay.env; kettu-relay link'
```

### Trying it on one computer

With none of `KETTU_RELAY_DOMAIN`, `KETTU_RELAY_URL` and `KETTU_RELAY_IP` set, the relay serves
plain http on `127.0.0.1:8798` only, for trying it out with Kettu on the same computer:

```sh
go run github.com/KettuAI/relay/cmd/kettu-relay@latest
```

Plain http is never served on any other address, since the access key crosses the network.

## Commands

| Command | What it does |
| --- | --- |
| `kettu-relay` | Runs the relay and prints its link. |
| `kettu-relay link` | Prints the link again. |
| `kettu-relay key reset` | Makes a new access key. A running relay cuts off the computers signed in with the old key within a few seconds; paste the new link into each. |
| `kettu-relay version` | Prints the version. |

In Docker, run them in the container: `docker exec kettu-relay kettu-relay link`.

## Settings

All settings are environment variables.

| Variable | Default | Meaning |
| --- | --- | --- |
| `KETTU_RELAY_DOMAIN` | | Serve https for this domain with a Let's Encrypt certificate. |
| `KETTU_RELAY_URL` | | Serve plain http behind your proxy; this is the https address people use. |
| `KETTU_RELAY_IP` | | Serve https at this IP address with a self-signed certificate. |
| `KETTU_RELAY_LISTEN` | `:443`, or `127.0.0.1:8798` behind a proxy or on one computer | The address to listen on. With a domain or an IP, a port other than 443 becomes part of the relay's address. |
| `KETTU_RELAY_HTTP_LISTEN` | `:80` | With a domain: Let's Encrypt's http challenge and the redirect to https. `off` for none. |
| `KETTU_RELAY_DATA` | `/data` in Docker, else the user's config directory | Where the key, certificates and web client are kept. |
| `KETTU_RELAY_ACCESS_KEY` | | The access key, instead of the key file. `kettu-relay key reset` cannot change it. |
| `KETTU_RELAY_ACME_EMAIL` | | An address Let's Encrypt may write to about the certificate. |
| `KETTU_RELAY_TRUSTED_PROXIES` | loopback behind a proxy, else none | Addresses and CIDR ranges whose `X-Forwarded-For` is believed. |
| `KETTU_RELAY_WEB` | `on` | `off` stops serving the web client. |
| `KETTU_RELAY_CONNECTIONS_PER_MINUTE` | `120` | New connections a minute from one address. |
| `KETTU_RELAY_DEVICES_PER_MINUTE` | `60` | New device connections a minute to one computer. |

Each computer may have 16 devices connected at once, a message may be up to 64 KiB, and a computer
or device has 10 seconds to sign in or connect, as the protocol sets.

## The web client

So that a browser can reach your computers through your relay, the relay serves Kettu's web client
at its own address: when it starts and every day after, it downloads the web client built for
relays from the latest [Kettu release](https://github.com/KettuAI/Kettu/releases), checks it against
the release's SHA-256 checksums and unpacks it into its data directory. When a download fails it
keeps serving the copy it has; before it ever got one, it serves none and says so in its log.

A computer's **Add device** then gives browsers a link to `https://<your relay>/pair#...`. The page
talks only to its own origin, the relay, and pairs and runs sessions like the hosted web client.
Whoever runs a relay can change the pages it serves, so a browser that opens the web client through
a relay trusts the relay's operator; on your own relay that is you. The desktop and iPhone apps do
not depend on it.

## Versions

kettu-relay follows [semantic versioning](https://semver.org): fixes are patch versions, new
features minor versions, and anything taken away a major version. It serves relay protocol 1,
described in [PROTOCOL.md](PROTOCOL.md), which only grows; see [CHANGELOG.md](CHANGELOG.md) for each
version.

## Building from source

```sh
go build ./cmd/kettu-relay
go test -race ./...
```

The relay uses Go's standard library, `golang.org/x/crypto` for Let's Encrypt and
`github.com/coder/websocket`. The package `github.com/KettuAI/relay/relay` is the relay itself,
with who may sign in left to an `Admission`, so other programs can build on it.

## License

[AGPL-3.0](LICENSE). Report security problems as described in [SECURITY.md](SECURITY.md).
