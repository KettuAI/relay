# Security

## Reporting a problem

Please report security problems privately, through
[Report a vulnerability](https://github.com/KettuAI/relay/security/advisories/new) on this
repository, not in a public issue. Say what you found, how to reproduce it and which version you ran.
You will get an answer within a week, and a fix is released before the problem is made public.

## What the relay is trusted with

- The relay passes Kettu's end-to-end encrypted channel and cannot read it. A computer decides
  inside the channel which devices it talks to, so a relay cannot let a device into a computer.
- The access key decides which computers may sign in. It is compared in constant time and kept
  only in a file readable by the relay's user (or in `KETTU_RELAY_ACCESS_KEY`); the relay refuses
  to start when that file is readable by others. The full link with the key is printed on the first
  start and by `kettu-relay link` alone; the log shows it masked, and a key from the environment is
  never printed. A key reset also refuses sign-ins that were checked against the old key while it
  changed.
- Devices bring no credential. They can only ask the relay to have a computer open a connection to
  them. Caps on the relay's connections and on each address's, rate limits per address and per
  computer, at most 16 devices per computer, a bounded queue of waiting devices and a deadline for
  a joined device the computer never answers keep them from filling a computer's slots or the
  relay; every limit is a setting.
- `X-Forwarded-For` is believed only from the proxies in `KETTU_RELAY_TRUSTED_PROXIES`. Behind a
  proxy the relay serves plain http only on loopback unless told otherwise, and then takes
  connections only from those proxies.
- The web client is downloaded only over https from GitHub's release hosts, or from Kettu's
  download service when GitHub cannot be reached, redirects included, and used only when it
  matches the release's SHA-256 checksum; the download, the unpacked size and
  the number of files are capped, and unpacking refuses paths outside its folder, links and special
  files. Whoever runs a relay controls the pages it serves.
- Private keys of certificates and of the Let's Encrypt account are kept in files readable by the
  relay's user alone, in a data directory kept at mode 0700. The container image runs as an
  unprivileged user and writes only to `/data`.
- Release workflows pin every action by commit and give each job only the permissions it needs.

## Supported versions

Fixes go into the latest release.
