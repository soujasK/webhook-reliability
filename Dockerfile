FROM golang:1.22-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server
RUN CGO_ENABLED=0 go build -o /out/mock-receiver ./cmd/mock-receiver

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /out/server /server
COPY --from=build /out/mock-receiver /mock-receiver
COPY frontend /frontend
# CMD, not ENTRYPOINT: with ENTRYPOINT set, a platform's "start command" override (Render's
# Docker Command, Railway's Start Command, docker-compose's `command:`) gets *appended to* the
# entrypoint rather than replacing it -- so overriding it to run /mock-receiver would actually
# execute `/server /mock-receiver`, silently launching the main server instead (Go's flag parser
# stops at the first non-flag token and ignores the rest). Plain CMD means any such override fully
# replaces this default, so pointing a second service at /mock-receiver actually runs it.
CMD ["/server", "--mode=all"]
