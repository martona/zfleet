#!/bin/bash
# Cross-compile zfse for linux/amd64 and ship it to commodoreplus4.
# Source stays local; only the binary travels.
set -euo pipefail
cd "$(dirname "$0")/.."

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/zfse-linux-amd64 ./cmd/zfse
ssh -o BatchMode=yes marton@commodoreplus4.lan 'mkdir -p ~/claude/bin'
scp -q -o BatchMode=yes dist/zfse-linux-amd64 marton@commodoreplus4.lan:claude/bin/zfse
ssh -o BatchMode=yes marton@commodoreplus4.lan 'chmod +x ~/claude/bin/zfse'
echo "deployed to commodoreplus4:~/claude/bin/zfse"
