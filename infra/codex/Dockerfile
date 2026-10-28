# Codex app-server for agent sessions. Version is pinned: the protocol is tested against it.
FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git && rm -rf /var/lib/apt/lists/*
RUN for i in 1 2 3 4 5; do npm install -g @openai/codex@0.163.0 && exit 0; sleep 3; done; exit 1
RUN useradd -m -u 10001 agent
USER agent
WORKDIR /workspace
ENTRYPOINT ["codex", "app-server", "--listen", "stdio://"]
