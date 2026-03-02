#!/usr/bin/env bash
# Publish binary package to AUR as basecamp-cli.
# Called from the release workflow after GoReleaser uploads assets.
# Usage: scripts/publish-aur.sh <version>
set -euo pipefail

VERSION="${1:?usage: publish-aur.sh <version>}"
PKGNAME="basecamp-cli"
AUR_REPO="ssh://aur@aur.archlinux.org/${PKGNAME}.git"

# Compute checksums from the GitHub release assets
base_url="https://github.com/basecamp/basecamp-cli/releases/download/v${VERSION}"
sha_x86=$(curl -sL "${base_url}/checksums.txt" | grep "linux_amd64\.tar\.gz$" | awk '{print $1}')
sha_arm=$(curl -sL "${base_url}/checksums.txt" | grep "linux_arm64\.tar\.gz$" | awk '{print $1}')

if [ -z "$sha_x86" ] || [ -z "$sha_arm" ]; then
  echo "ERROR: could not find linux checksums in release assets" >&2
  exit 1
fi

# Generate PKGBUILD
pkgbuild=$(cat <<PKGBUILD
# Maintainer: Basecamp <support@basecamp.com>
pkgname=${PKGNAME}
pkgver=${VERSION}
pkgrel=1
pkgdesc="CLI for Basecamp project management"
arch=('x86_64' 'aarch64')
url="https://github.com/basecamp/basecamp-cli"
license=('MIT')
provides=('basecamp')
conflicts=('basecamp' 'basecamp-bin')
optdepends=(
  'bash-completion: for bash shell completions'
  'zsh: for zsh shell completions'
  'fish: for fish shell completions'
)
source_x86_64=("${base_url}/basecamp_\${pkgver}_linux_amd64.tar.gz")
source_aarch64=("${base_url}/basecamp_\${pkgver}_linux_arm64.tar.gz")
sha256sums_x86_64=('${sha_x86}')
sha256sums_aarch64=('${sha_arm}')

package() {
  install -Dm755 "basecamp" "\${pkgdir}/usr/bin/basecamp"
  install -Dm644 "MIT-LICENSE" "\${pkgdir}/usr/share/licenses/basecamp/MIT-LICENSE"
  install -Dm644 "completions/basecamp.bash" "\${pkgdir}/usr/share/bash-completion/completions/basecamp"
  install -Dm644 "completions/_basecamp" "\${pkgdir}/usr/share/zsh/site-functions/_basecamp"
  install -Dm644 "completions/basecamp.fish" "\${pkgdir}/usr/share/fish/vendor_completions.d/basecamp.fish"
}
PKGBUILD
)

# Generate .SRCINFO
srcinfo=$(cat <<SRCINFO
pkgbase = ${PKGNAME}
	pkgdesc = CLI for Basecamp project management
	pkgver = ${VERSION}
	pkgrel = 1
	url = https://github.com/basecamp/basecamp-cli
	arch = x86_64
	arch = aarch64
	license = MIT
	provides = basecamp
	conflicts = basecamp
	conflicts = basecamp-bin
	optdepends = bash-completion: for bash shell completions
	optdepends = zsh: for zsh shell completions
	optdepends = fish: for fish shell completions
	source_x86_64 = ${base_url}/basecamp_${VERSION}_linux_amd64.tar.gz
	sha256sums_x86_64 = ${sha_x86}
	source_aarch64 = ${base_url}/basecamp_${VERSION}_linux_arm64.tar.gz
	sha256sums_aarch64 = ${sha_arm}

pkgname = ${PKGNAME}
SRCINFO
)

# Clone, update, push
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

git clone "$AUR_REPO" "$workdir/aur"
cd "$workdir/aur"

echo "$pkgbuild" > PKGBUILD
echo "$srcinfo" > .SRCINFO

git add PKGBUILD .SRCINFO
if git diff --cached --quiet; then
  echo "AUR already up to date"
  exit 0
fi

git commit -m "Update to v${VERSION}"
git push origin master
echo "Published ${PKGNAME} v${VERSION} to AUR"
