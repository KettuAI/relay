# Security

## Reporting a problem

Please report security problems privately, through
[Report a vulnerability](https://github.com/KettuAI/relay/security/advisories/new) on this
repository, not in a public issue. Say what you found, how to reproduce it and which version you ran.
You will get an answer within a week, and a fix is released before the problem is made public.

## What the relay is trusted with

- The relay passes Kettu's end-to-end encrypted channel and cannot read it. A computer decides
  inside the channel which devices it talks to, so a relay cannot let a device into a computer.
- The access key decides which computers may sign in. It is compared in constant time, kept only
  in a file readable by the relay's user (or in `KETTU_RELAY_ACCESS_KEY`) and never written to the
  log or an error.
- Devices bring no credential. They can only ask the relay to have a computer open a connection to
  them; rate limits per address and per computer, and at most 16 devices per computer at once, keep
  them from flooding it.
- `X-Forwarded-For` is believed only from the proxies in `KETTU_RELAY_TRUSTED_PROXIES`.
- The web client is downloaded only from the Kettu releases on GitHub and used only when it matches
  the release's SHA-256 checksum; unpacking refuses paths outside its folder, links and special
  files. Whoever runs a relay controls the pages it serves.
- Private keys of certificates and of the Let's Encrypt account are kept in files readable by the
  relay's user alone. The container image runs as an unprivileged user and writes only to `/data`.

## Supported versions

Fixes go into the latest release.
