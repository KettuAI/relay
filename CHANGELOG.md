# Changelog

All notable changes to kettu-relay. Versions follow [semantic versioning](https://semver.org).

## [Unreleased]

## [0.1.3] - 2026-11-28

- `kettu-relay web update` checks for a newer Kettu web client immediately and hot-reloads it
  without restarting a running relay or dropping active connections, or updates the copy on disk
  when the relay is stopped.

## [0.1.2] - 2026-11-25

- `install.sh` downloads from Kettu's download service and falls back to GitHub, so a server that
  cannot reach GitHub can install and update the relay; it is served at
  `https://kettu.sh/relay/install`. It takes the service unit from the archive, which now carries
  it under `deploy/`, instead of from raw.githubusercontent.com.
- When GitHub cannot be reached, the web client is downloaded from Kettu's download service,
  checked against the same release's SHA-256 checksums.

## [0.1.1] - 2026-11-25

- Serves the web client's artifact frames, `/_kettu/frame`, `/_kettu/frame-strict` and
  `/_kettu/frame-viewer`, with an isolated sandbox CSP and Permissions-Policy, and its viewer files
  under `/_kettu/viewer/` with read-only CORS headers, so artifacts open live in the web client
  the relay serves (Kettu 0.1.21 or later).
- The `relay` package gives programs built on it more to work with: `Hosts` lists the signed-in
  hosts with their last profile, `Stats` counts hosts, devices, pairs and bytes, `Undrain` ends a
  drain, `CloseAll` closes every socket without stopping, and an Admission that implements
  `DeviceGrants` gives each device a grant of its own, which `Revoke` matches.
- `install.sh` installs the latest release on Linux and macOS after checking it against
  `SHA256SUMS`, and on Linux with systemd sets up the `kettu-relay` service and prints the link.

## [0.1.0] - 2026-11-24

The first version.

- Serves Kettu relay protocol 1 with the `access-key` and `drain` features: computers sign in with
  the relay's access key, devices bring no token.
- Makes its access key on first start, or takes `KETTU_RELAY_ACCESS_KEY`; `kettu-relay key reset`
  makes a new one and cuts off the computers signed in with the old one.
- Certificates three ways: Let's Encrypt for a domain, your own proxy in front, or a self-signed
  certificate pinned by clients at an IP address. Plain http only on a loopback address.
- Prints a link with the address and the key, and the pin at an IP address, for Kettu's settings,
  on the first start; later starts log it with the key masked, and `kettu-relay link` prints it in
  full.
- Bounds what devices without a key can hold: caps on the relay's connections and each address's,
  a queue of waiting devices per computer, and a deadline for a device the computer never answers,
  all of them settings.
- Behind a proxy, listens on loopback unless told otherwise and takes connections only from the
  trusted proxies. Refuses to start with a key file or private key others can read.
- Serves the Kettu web client from the latest Kettu release, checked against its SHA-256, with the
  `web` feature.
- Archives for Linux, macOS and Windows, each on amd64 and arm64, with the license and
  third-party notices, and the image `ghcr.io/kettuai/relay` for Linux on amd64 and arm64.
