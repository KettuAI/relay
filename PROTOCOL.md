# Kettu relay protocol 1

This is the protocol between Kettu clients and a Kettu relay, as kettu-relay serves it. Every relay
of protocol 1 speaks the same messages; relays differ only in who they let sign in and in the
features they add. The test vectors are in [`relay/testdata`](relay/testdata).

A **host** is a Kettu computer: the desktop app or the daemon. A **device** is anything that
connects to a host: the iPhone app, a browser, another computer. The relay joins a device's socket
to a socket the host opens for it and then passes binary messages between them unchanged. They are
the Noise messages of Kettu's encrypted channel; the relay cannot read them.

## For client developers

The facts a client of a self-hosted relay relies on:

- **Discovery.** `GET /v1` answers `{"relay":"kettu","protocol":1,"regions":[],"features":[...],"version":"0.1.0"}`.
  A self-hosted relay lists `access-key` and `drain`, and `web` while it serves the web client.
  `regions` is always an empty array. `version` is the program's version and may be missing on
  other relays.
- **Host sign-in.** The host puts the relay's access key in the sign-in message's existing `token`
  field: `{"type":"sign-in","publicKey":"...","signature":"...","token":"krk_..."}`. The relay
  compares it in constant time; a wrong key closes the socket with 4401.
- **Devices.** A device sends `{"type":"connect","features":["drain"]}` with **no `token` field**.
  The relay also takes an empty or any other token and ignores it. A device must never send a Kettu
  account token to a relay it does not trust for one.
- **The link.** `kettu-relay` prints `<origin>#key=<access key>`, plus `&pin=<pin>` for a relay at
  an IP address: `https://relay.example.com#key=krk_...` or
  `https://203.0.113.7#key=krk_...&pin=...`. The origin is `https://host[:port]` with no path; it
  is `http://` only on a loopback address. The key is `krk_` followed by 43 characters of base64url
  (32 bytes). Unknown fields after `#` are ignored.
- **The pin.** A relay at an IP address has a self-signed certificate. Its pin is the SHA-256 of the
  certificate's SubjectPublicKeyInfo in DER, in base64url without padding (43 characters), the same
  pin clients already use for relays reached by IP. A client checks the pin alone, not the
  certificate's issuer, name or dates.
- **The signed origin** is the origin in the link, exactly as printed.

## Discovery

`GET /v1` returns JSON, readable from any origin (`Access-Control-Allow-Origin: *`):

| Field | Meaning |
| --- | --- |
| `relay` | Always `"kettu"`. |
| `protocol` | `1`. A client refuses a relay of another protocol. |
| `regions` | Other relays a client may use. Always `[]` on a self-hosted relay; clients require the field. |
| `features` | What the relay offers beyond the protocol; see Features. |
| `version` | The relay program's version, when it names one. |

`GET /v1/ping` answers 204 with no body, for timing a round trip.

## Sockets

All sockets are WebSockets without compression. Text messages are JSON objects except `ping`. A
message, text or binary, is at most 64 KiB; a longer one closes the socket with 1009. Any socket may
send the text `ping` at any time and gets the text `pong`; the relay also sends WebSocket pings and
drops a socket that does not answer.

A host's id is the first 16 bytes of the SHA-256 of its Ed25519 public key, in lowercase base32
without padding: 26 characters of `a-z2-7`. Tokens and nonces are 32 random bytes in base64url.

### Host control socket: `/v1/hosts/<id>`

1. The relay sends `{"type":"challenge","nonce":"<nonce>"}`.
2. Within 10 seconds the host sends
   `{"type":"sign-in","publicKey":"<key>","signature":"<signature>","token":"<credential>"}`, with
   its raw 32-byte Ed25519 public key and a signature over the UTF-8 text

   ```
   kettu-relay/1
   <relay origin>
   <id>
   <nonce>
   ```

   (four lines joined by `\n`, no trailing newline). The origin is the relay's, as the host reached
   it, so a signature cannot be replayed to another relay, id or challenge. The relay checks that
   the key is the id's and the signature verifies, then the credential.
3. The relay sends `{"type":"ready"}`. Another sign-in for the same id later takes this one's place:
   the old socket gets `{"type":"replaced"}` and is closed with 4409.
4. For each device that asks for the host, the relay sends `{"type":"incoming","token":"<token>"}`,
   and the host has 10 seconds to open the accept socket.

Once ready, a host may send `{"type":"token","token":"<credential>"}` to renew its credential, and
`{"type":"profile",...}`, which only relays with the `hosts` feature use and others drop. Anything
else, or anything before sign-in, closes the socket with 4400.

### Device socket: `/v1/hosts/<id>/connect`

1. Within 10 seconds the device sends `{"type":"connect","token":"<credential>","features":[...]}`.
   The token is what the relay's admission asks for; a self-hosted relay asks for none. The only
   device feature is `drain`.
2. When the host is signed in, the relay tells it `incoming`; once the host opened the accept
   socket, the device gets `{"type":"joined"}`.
3. From then on binary messages pass both ways. A text message other than `ping` closes both
   sockets with 4400. Either side closing closes the other with 1000, or with the relay's code
   when the relay closed it.

### Accept socket: `/v1/hosts/<id>/accept/<token>`

The host opens it with an `incoming` token. It is joined at once to the device and passes binary
messages. A token that is not one waiting closes it with 4404.

## Close codes

| Code | Reason | When |
| --- | --- | --- |
| 1000 | | The other end of a pair went. |
| 1001 | | The relay is stopping. |
| 1009 | `too_big` | A message over 64 KiB. |
| 1011 | | The relay could not check a credential; try again. |
| 4400 | `malformed` | A message that is malformed or out of turn. |
| 4401 | `unauthorized` | A key that is not the id's, a bad signature, or a credential not taken, such as a wrong or reset access key. |
| 4403 | `forbidden` | A device the relay's admission refuses, or one joined through a host whose credential was revoked. |
| 4404 | `offline` | The host is not signed in, did not accept in time, or the accept token is not one. |
| 4408 | `timeout` | No sign-in or connect within 10 seconds. |
| 4409 | `replaced` | Another sign-in for the same id took this one's place. |
| 4429 | `too_many` | Too many connections from an address or to a host in a minute, or more than 16 devices at once. |

A refused upgrade is accepted and closed with its code, since a browser cannot read an HTTP status
on a failed WebSocket. A request on a socket path that is not an upgrade gets 426; an unknown path
gets 404.

## Features

| Feature | Meaning |
| --- | --- |
| `access-key` | Hosts sign in with the relay's access key in `token`; devices bring no token. |
| `drain` | The relay is about to stop and tells ready hosts, and joined devices whose `connect` listed `drain`, `{"type":"drain"}`. They move elsewhere and close their sockets themselves. |
| `web` | The relay serves the Kettu web client at its own origin, with the pairing page at `/pair`. |

Kettu's own relays list further features, which a self-hosted relay does not have: `hosts`,
`regions`, `relay-tokens` and `reports`.

## Changing the protocol

Protocol 1, everything under `/v1`, only grows. Fields, messages and close codes keep their meaning;
something new is added with a feature in `GET /v1`, and a side that does not know it keeps working.
Every side ignores fields, messages and features it does not know. A change that cannot be made that
way is protocol 2, under `/v2`; relays then serve `/v1` beside it for at least 12 months, and
clients keep speaking 1 for at least 12 months after relays can speak 2. kettu-relay takes nothing
away except in a major version after those 12 months.
