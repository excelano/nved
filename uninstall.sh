#!/bin/sh
# nved uninstaller — finds and removes the nved binary. nved keeps no config
# or history on disk, so removing the binary is the entire cleanup. POSIX sh,
# no bash extensions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/excelano/nved/main/uninstall.sh | sh
#
# Environment variables:
#   NVED_UNINSTALL_YES=1  Skip the binary-removal confirmation (assume yes).

set -eu

say() { printf '%s\n' "$*" >&2; }
err() { say "error: $*"; exit 1; }

# read_yes reads a y/N answer from the controlling terminal, not stdin,
# because this script is typically invoked as `curl ... | sh` where stdin
# is the script itself.
read_yes() {
	prompt="$1"
	if [ "${NVED_UNINSTALL_YES:-0}" = "1" ]; then
		return 0
	fi
	if [ ! -t 0 ] && [ ! -e /dev/tty ]; then
		err "no terminal available for confirmation; re-run with NVED_UNINSTALL_YES=1 to skip the prompt"
	fi
	printf '%s [y/N]: ' "$prompt" >&2
	if [ -e /dev/tty ]; then
		read ans </dev/tty
	else
		read ans
	fi
	case "$ans" in
		y|Y|yes|YES) return 0 ;;
		*) return 1 ;;
	esac
}

if ! command -v nved >/dev/null 2>&1; then
	say "nved is not on PATH; nothing to uninstall."
	say "If you installed to a custom location, remove it manually:"
	say "    rm /path/to/nved"
	exit 0
fi

TARGET=$(command -v nved)
say "Found nved at $TARGET"

if [ ! -w "$TARGET" ] && [ ! -w "$(dirname "$TARGET")" ]; then
	err "$TARGET is not writable; re-run with sudo to remove it"
fi

if ! read_yes "Remove $TARGET?"; then
	say "Aborted."
	exit 1
fi

rm -f "$TARGET" || err "could not remove $TARGET"
say "Removed $TARGET"

# Invalidate the shell's command hash; without this, `command -v` happily
# reports the just-deleted path as still present and the duplicate-install
# check below cries wolf.
hash -r 2>/dev/null || true

# Check for additional installs (e.g. one in /usr/local/bin plus one in
# ~/.local/bin). PATH lookup only finds the first; warn so the user knows
# the others remain.
LEFTOVER=$(command -v nved 2>/dev/null || true)
if [ -n "$LEFTOVER" ]; then
	say ""
	say "Note: another nved binary is still on PATH at $LEFTOVER"
	say "Re-run this uninstaller to remove it, or remove it manually."
fi

say ""
say "Done."
