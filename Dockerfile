# Artifacts are built on the host by `make build` (Go binary for linux and web/dist),
# which keeps the Docker disk free of node_modules and Go build caches.
FROM docker:27-cli AS dockercli

# Root: the backend starts agent containers through the host Docker socket.
FROM gcr.io/distroless/static-debian12
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY bin/server-linux /server
COPY web/dist /web
ENV WEB_DIR=/web HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/server"]
