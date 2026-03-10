FROM mcr.microsoft.com/devcontainers/go:1-bookworm

# Docker creates named volume mount points as root:root. Pre-create all volume
# targets here so the vscode user owns them before any container process runs.
RUN mkdir -p /commandhistory \
    && touch /commandhistory/.bash_history \
    && chown -R vscode:vscode /commandhistory \
    && mkdir -p /home/vscode/.vscode-server/data/User/globalStorage/github.copilot-chat \
    && chown -R vscode:vscode /home/vscode/.vscode-server
