# Changelog

All notable changes to kettu-relay. Versions follow [semantic versioning](https://semver.org).

## [0.1.0] - Unreleased

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
- Archives for Linux, macOS and Windows with the license and third-party notices, and the image
  `ghcr.io/kettuai/relay`.
