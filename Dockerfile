# --- build stage ---
FROM golang:1.26.4-alpine AS build
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

# --- runtime stage ---
FROM scratch
COPY --from=build /out/tubctl /tubctl
EXPOSE 3000
ENTRYPOINT ["/tubctl"]
CMD ["serve"]
