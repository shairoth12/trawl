#!/bin/bash
set -euo pipefail

# Install golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)/bin"

# Install additional Go tools
go install mvdan.cc/gofumpt@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/cweill/gotests/gotests@latest
go install github.com/fatih/gomodifytags@latest
go install github.com/josharian/impl@latest

# Resolve dependencies and generate go.sum
go mod tidy

# Persist shell history across container rebuilds via the mounted volume
mkdir -p /commandhistory
touch /commandhistory/.bash_history
echo 'export HISTFILE=/commandhistory/.bash_history' >> ~/.bashrc
echo 'export PROMPT_COMMAND="history -a"' >> ~/.bashrc

# Ensure agent data volumes are owned by the current user (Docker creates
# new named volumes as root; this is a no-op after the first build)
sudo chown -R vscode:vscode ~/.claude \
  ~/.vscode-server/data/User/globalStorage/github.copilot-chat \
  2>/dev/null || true
