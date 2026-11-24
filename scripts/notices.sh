#!/bin/sh
# Prints the license notices of the code built into kettu-relay: Go's own and each module's.
# CI checks that THIRD_PARTY_NOTICES.txt matches: scripts/notices.sh > THIRD_PARTY_NOTICES.txt
set -eu
cd "$(dirname "$0")/.."

notice() {
	name=$1 dir=$2
	file=
	for f in LICENSE LICENSE.txt LICENSE.md COPYING; do
		if [ -f "$dir/$f" ]; then file="$dir/$f"; break; fi
	done
	if [ -z "$file" ]; then
		echo "no license file for $name in $dir" >&2
		exit 1
	fi
	printf '%s\n%s\n\n' "================================================================================" "$name"
	cat "$file"
	printf '\n'
}

echo "kettu-relay is licensed under the GNU Affero General Public License v3.0 (see LICENSE)."
echo "It includes the following third-party code, under the licenses below."
echo
notice "The Go programming language (standard library and runtime)" "$(go env GOROOT)"
go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' ./cmd/kettu-relay |
	sort -u |
	while read -r path dir; do
		notice "$path" "$dir"
	done
