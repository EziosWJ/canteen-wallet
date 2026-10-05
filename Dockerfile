FROM node:22-bookworm-slim AS web-build

WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci

COPY web ./web
COPY assets/ui ./assets/ui
RUN mkdir -p web/public/assets/ui \
    && cp assets/ui/*.png web/public/assets/ui/ \
    && npm --prefix web run build

FROM golang:1.26-bookworm AS server-build

WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download

COPY backend ./backend
COPY --from=web-build /src/web/dist ./backend/internal/webassets/dist
WORKDIR /src/backend
RUN CGO_ENABLED=0 GOOS=linux go build -tags=timetzdata -trimpath -ldflags="-s -w" -o /out/canteen-server ./cmd/server

FROM debian:bookworm-slim

RUN mkdir -p /var/lib/canteen /mnt/canteen-backup \
    && chown 10001:10001 /var/lib/canteen /mnt/canteen-backup

COPY --from=server-build --chown=10001:10001 /out/canteen-server /usr/local/bin/canteen-server
COPY --from=server-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENV CANTEEN_PUBLIC_ADDR=127.0.0.1:5001 \
    CANTEEN_INTERNAL_ADDR=127.0.0.1:8081 \
    CANTEEN_DB_PATH=/var/lib/canteen/canteen.db \
    CANTEEN_TIME_ZONE=Asia/Shanghai

USER 10001:10001
EXPOSE 5001 8081
ENTRYPOINT ["/usr/local/bin/canteen-server"]
