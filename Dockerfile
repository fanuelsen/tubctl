# --- build stage ---
FROM golang:1.27.1-alpine AS build
WORKDIR /src
# go.mod/go.sum first for layer caching
COPY go.mod ./
# (no go.sum yet — no deps outside stdlib)
COPY . .
# static, stripped binary; embed.FS pulls in internal/web/public at compile time
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/tubctl ./cmd/tubctl
# /data is copied into the runtime image so a fresh named volume inherits
# nobody-writable ownership (scratch has no mkdir/chown to fix it later).
RUN mkdir -p /out/data

# --- runtime stage ---
FROM scratch
COPY --from=build /out/tubctl /tubctl
COPY --from=build --chown=65534:65534 /out/data /data
# nobody:nogroup — the server needs no privileges; it only dials the tub and
# writes /data/schedules.json. NOTE: a named volume created by an older (root)
# image keeps root ownership; chown it to 65534 once when upgrading.
USER 65534:65534
EXPOSE 3000
ENTRYPOINT ["/tubctl"]
CMD ["serve"]
