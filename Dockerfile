FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN for i in 1 2 3 4 5; do npm ci && exit 0; sleep 3; done; exit 1
COPY web/ ./
COPY contracts/ /src/contracts/
RUN npm run build

FROM golang:1.27-bookworm AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN for i in 1 2 3 4 5; do go mod download && exit 0; sleep 3; done; exit 1
COPY contracts/ contracts/
COPY backend/ backend/
RUN CGO_ENABLED=0 go build -o /server ./backend/cmd/server

FROM docker:27-cli AS dockercli

# Root: the backend starts agent containers through the host Docker socket.
FROM gcr.io/distroless/static-debian12
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=server /server /server
COPY --from=web /src/web/dist /web
ENV WEB_DIR=/web HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/server"]
