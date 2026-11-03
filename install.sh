#!/usr/bin/env bash
# Installs jenkinsctl and its Claude Code skill from GitHub releases:
#   curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash
# Everything runs inside main so a truncated download executes nothing.
set -euo pipefail

repo="priyanshu-shubham/jenkinsctl"
tmp=""

usage() {
	cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--project DIR] [--uninstall]

Installs the jenkinsctl binary into $BIN_DIR (default ~/.local/bin) and the
Claude Code skill into ~/.claude/skills/jenkinsctl (respects $CLAUDE_CONFIG_DIR).

  --version V     install release V instead of the latest
  --project DIR   install the skill into DIR/.claude/skills/jenkinsctl instead,
                  so it is shared with everyone working in that repository
  --uninstall     remove the binary and the skill (saved servers are kept)

With curl, pass options after "bash -s --", e.g.:
  curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash -s -- --project .
EOF
}

die() {
	echo "error: $*" >&2
	exit 1
}

sha256_check() {
	if command -v sha256sum >/dev/null; then
		sha256sum -c --status
	else
		shasum -a 256 -c --status
	fi
}

main() {
	local bin_dir="${BIN_DIR:-$HOME/.local/bin}"
	local skill_root="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills"
	local version="latest" uninstall=0

	while [[ $# -gt 0 ]]; do
		case "$1" in
		--version)
			[[ $# -ge 2 ]] || die "--version needs a value"
			version="$2"
			shift 2
			;;
		--project)
			[[ $# -ge 2 ]] || die "--project needs a directory"
			skill_root="$(cd "$2" && pwd)/.claude/skills" || die "no such directory: $2"
			shift 2
			;;
		--uninstall) uninstall=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*) usage >&2; exit 2 ;;
		esac
	done
	local skill_dir="$skill_root/jenkinsctl"

	if [[ $uninstall -eq 1 ]]; then
		rm -f "$bin_dir/jenkinsctl"
		rm -rf "$skill_dir"
		echo "Removed $bin_dir/jenkinsctl and $skill_dir"
		echo "Saved servers (with tokens) remain in ~/.config/jenkinsctl; delete it to remove them."
		return
	fi

	local os arch
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported OS $(uname -s); on Windows use WSL or download a zip from https://github.com/$repo/releases" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture $(uname -m)" ;;
	esac
	command -v curl >/dev/null || die "curl is required"

	local base="https://github.com/$repo/releases/download/$version"
	[[ $version == latest ]] && base="https://github.com/$repo/releases/latest/download"
	# Lets tests and mirrors serve release assets from elsewhere.
	base="${JENKINSCTL_RELEASE_URL:-$base}"

	local name="jenkinsctl_${os}_${arch}"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	echo "Downloading $name ($version)..."
	curl -fsSL "$base/$name.tar.gz" -o "$tmp/$name.tar.gz" || die "download failed: $base/$name.tar.gz"
	curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"
	(cd "$tmp" && grep " $name.tar.gz\$" checksums.txt | sha256_check) || die "checksum mismatch for $name.tar.gz"
	tar -xzf "$tmp/$name.tar.gz" -C "$tmp"

	mkdir -p "$bin_dir" "$skill_dir"
	install -m 0755 "$tmp/$name/jenkinsctl" "$bin_dir/jenkinsctl"
	install -m 0644 "$tmp/$name/SKILL.md" "$skill_dir/SKILL.md"
	echo "Installed $("$bin_dir/jenkinsctl" --version) to $bin_dir/jenkinsctl"
	echo "Installed Claude Code skill to $skill_dir"

	case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) echo "warning: $bin_dir is not on PATH; add it so your shell and Claude Code can find jenkinsctl" >&2 ;;
	esac

	echo
	echo "Next:"
	echo "  1. jenkinsctl auth login     # add a Jenkins server (interactive)"
	echo "  2. start a new Claude Code session to load the skill"
}

main "$@"
