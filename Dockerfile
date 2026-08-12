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
ENTRYPOINT ["/server"]
