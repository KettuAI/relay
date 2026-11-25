#!/bin/sh
# Installs kettu-relay from Kettu's download service, or from its GitHub releases when that fails.
# On Linux with systemd it also sets up the kettu-relay service (run it as root); elsewhere, or
# with --no-service, it installs the program.
#
#   curl -fsSL https://kettu.sh/relay/install | sudo sh -s -- --domain relay.example.com
#
# Running it again updates the program and keeps the settings and the access key.
set -eu

repo=KettuAI/relay
# Laid out alike, so a file is at the same path below each; tried in this order.
sources="https://download.kettu.sh/relay https://github.com/$repo/releases"
version=
setting=
service=yes
env_file=/etc/kettu-relay.env
data_dir=/var/lib/kettu-relay

usage() {
	cat <<'EOF'
Usage: install.sh [options]

  --domain NAME    serve https for NAME, with a Let's Encrypt certificate
  --ip ADDRESS     serve https at ADDRESS, with a self-signed certificate
  --url URL        serve plain http on 127.0.0.1:8798 behind your own https proxy at URL
  --version X.Y.Z  install this version instead of the latest
  --no-service     install the program only, without the systemd service

With systemd, give one of --domain, --ip and --url the first time. Settings are kept in
/etc/kettu-relay.env. Running it again updates the program and keeps settings and key.
EOF
}

say() { printf '%s\n' "$*"; }
die() {
	printf 'kettu-relay install: %s\n' "$*" >&2
	exit 1
}

# Values go into the settings file, so only plain address characters are taken.
plain() {
	case $2 in
	'' | *[!A-Za-z0-9.:/_-]*) die "$1 takes a plain address, not '$2'" ;;
	esac
}

take() {
	[ -z "$setting" ] || die "give only one of --domain, --ip and --url"
	setting="$1=$2"
}

while [ $# -gt 0 ]; do
	case $1 in
	--domain | --ip | --url)
		[ $# -ge 2 ] || die "$1 needs a value"
		plain "$1" "$2"
		case $1 in
		--domain) take KETTU_RELAY_DOMAIN "$2" ;;
		--ip) take KETTU_RELAY_IP "$2" ;;
		--url) take KETTU_RELAY_URL "$2" ;;
		esac
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		version=${2#v}
		shift 2
		;;
	--no-service)
		service=no
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "this script installs on Linux and macOS; on Windows, download the zip from https://github.com/$repo/releases" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "there is no build for $(uname -m)" ;;
esac

if [ "$service" = yes ] && [ "$os" = linux ] && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	[ "$(id -u)" -eq 0 ] || die "run it as root (sudo sh) to set up the service, or pass --no-service"
else
	service=no
fi

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q --https-only -O "$2" "$1"; }
else
	die "it needs curl or wget"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{ print $1 }'; }
else
	sha256() { shasum -a 256 "$1" | awk '{ print $1 }'; }
fi

case $version in
*[!0-9A-Za-z.-]*) die "not a version: '$version'" ;;
esac
wanted=$version

# Downloads the release from one source into $tmp; the latest one's version is read from its
# SHA256SUMS, which names every archive, and the archive is then taken by that version.
from() {
	if [ -n "$wanted" ]; then
		sums_url="$1/download/v$wanted/SHA256SUMS"
	else
		sums_url="$1/latest/download/SHA256SUMS"
	fi
	fetch "$sums_url" "$tmp/SHA256SUMS" || {
		say "Could not download SHA256SUMS from $1"
		return 1
	}
	version=$wanted
	if [ -z "$version" ]; then
		version=$(sed -n "s/^[0-9a-fA-F]\{64\}  *\*\{0,1\}kettu-relay-\(.*\)-$os-$arch\.tar\.gz\$/\1/p" "$tmp/SHA256SUMS" | head -n 1)
	fi
	case $version in
	'' | *[!0-9A-Za-z.-]*)
		say "$1 lists no build for $os/$arch"
		return 1
		;;
	esac
	name="kettu-relay-$version-$os-$arch"
	say "Downloading kettu-relay $version for $os/$arch from $1"
	fetch "$1/download/v$version/$name.tar.gz" "$tmp/$name.tar.gz" || {
		say "Could not download $name.tar.gz from $1"
		return 1
	}
	want=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$want" ] && [ "$(sha256 "$tmp/$name.tar.gz")" = "$want" ] || {
		say "$name.tar.gz from $1 does not match SHA256SUMS"
		return 1
	}
}

got=no
for source in $sources; do
	if from "$source"; then
		got=yes
		break
	fi
done
[ "$got" = yes ] || die "could not download kettu-relay from any source"
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
program="$tmp/$name/kettu-relay"

if [ "$service" = no ]; then
	if [ "$(id -u)" -eq 0 ] || [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir="$HOME/.local/bin"
	fi
	mkdir -p "$dir"
	install -m 0755 "$program" "$dir/kettu-relay"
	say "Installed $dir/kettu-relay"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "Add $dir to your PATH to run it by name." ;;
	esac
	say ""
	say "Next: run it with KETTU_RELAY_DOMAIN, KETTU_RELAY_URL or KETTU_RELAY_IP set, and print its"
	say "link for Kettu with 'kettu-relay link'. How: https://github.com/$repo/blob/main/docs/deploy.md"
	exit 0
fi

id kettu-relay >/dev/null 2>&1 ||
	useradd --system --no-create-home --shell "$(command -v nologin || echo /usr/sbin/nologin)" kettu-relay
install -m 0755 "$program" /usr/local/bin/kettu-relay
unit="$tmp/$name/deploy/kettu-relay.service"
if [ ! -f "$unit" ]; then
	# Archives before 0.1.2 carry no service unit.
	unit="$tmp/kettu-relay.service"
	fetch "https://raw.githubusercontent.com/$repo/v$version/deploy/kettu-relay.service" "$unit" ||
		die "could not download the service unit"
fi
install -m 0644 "$unit" /etc/systemd/system/kettu-relay.service
install -d -m 0700 -o kettu-relay -g kettu-relay "$data_dir"

if [ ! -f "$env_file" ]; then
	if [ -n "$setting" ]; then
		printf '%s\nKETTU_RELAY_DATA=%s\n' "$setting" "$data_dir" >"$tmp/env"
	else
		cat >"$tmp/env" <<EOF
# Set one of these, then: systemctl enable --now kettu-relay
#KETTU_RELAY_DOMAIN=relay.example.com
#KETTU_RELAY_URL=https://relay.example.com
#KETTU_RELAY_IP=203.0.113.7
KETTU_RELAY_DATA=$data_dir
EOF
	fi
	install -m 0640 -o root -g kettu-relay "$tmp/env" "$env_file"
elif [ -n "$setting" ]; then
	if grep -Eq '^KETTU_RELAY_(DOMAIN|URL|IP)=' "$env_file"; then
		say "Kept the settings in $env_file; edit it to change the address."
	else
		printf '%s\n' "$setting" >>"$env_file"
	fi
fi
systemctl daemon-reload

if ! grep -Eq '^KETTU_RELAY_(DOMAIN|URL|IP)=' "$env_file"; then
	say ""
	say "Installed kettu-relay $version. Set the relay's address in $env_file, then run:"
	say "  systemctl enable --now kettu-relay"
	exit 0
fi

# The full link is printed only when the key is new, as the relay itself does.
new_key=yes
[ -f "$data_dir/access-key" ] && new_key=no
link=$(su -s /bin/sh kettu-relay -c "set -a; . $env_file; /usr/local/bin/kettu-relay link") ||
	die "kettu-relay could not read its settings in $env_file"
systemctl enable kettu-relay >/dev/null 2>&1
systemctl restart kettu-relay

say ""
say "kettu-relay $version is running as the kettu-relay service."
if [ "$new_key" = yes ]; then
	say "Paste this link into Kettu, in Settings > Remote access > Relay > Your own relay:"
	say ""
	say "  $link"
	say ""
	say "It holds the relay's access key; keep it to yourself."
else
	say "Print the link for Kettu again with:"
	say "  sudo su -s /bin/sh kettu-relay -c 'set -a; . $env_file; kettu-relay link'"
fi
