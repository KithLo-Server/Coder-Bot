FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/opencode-bot ./cmd/bot

FROM docker:27-dind

RUN apk add --no-cache ca-certificates git bash openssh-client tar

WORKDIR /app

COPY --from=build /out/opencode-bot /app/opencode-bot
COPY --chmod=0755 base-entrypoint.sh /usr/local/bin/base-entrypoint.sh

ENTRYPOINT ["/usr/local/bin/base-entrypoint.sh"]
CMD ["/app/opencode-bot"]
