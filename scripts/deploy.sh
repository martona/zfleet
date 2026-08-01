#!/bin/bash
# Cross-compile zfleet for linux/amd64 and ship it to commodoreplus4.
# Source stays local; only the binary travels.
set -euo pipefail
cd "$(dirname "$0")/.."

# stamp the corner/--version with git describe so a hand-deployed binary is
# distinguishable from a release ("0.1.0-3-g945fcdc" vs the tag's "0.1.0")
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$VERSION" -o dist/zfleet-linux-amd64 ./cmd/zfleet
ssh -o BatchMode=yes marton@commodoreplus4.lan 'mkdir -p ~/claude/bin'
# upload to a temp name, then rename: replacing a running binary directly
# fails with "text file busy", but rename swaps it out cleanly
scp -q -o BatchMode=yes dist/zfleet-linux-amd64 marton@commodoreplus4.lan:claude/bin/zfleet.new
# zfse symlink: a month of muscle memory, then delete
ssh -o BatchMode=yes marton@commodoreplus4.lan 'chmod +x ~/claude/bin/zfleet.new && mv ~/claude/bin/zfleet.new ~/claude/bin/zfleet && ln -sf zfleet ~/claude/bin/zfse'
echo "deployed to commodoreplus4:~/claude/bin/zfleet (+zfse symlink)"
