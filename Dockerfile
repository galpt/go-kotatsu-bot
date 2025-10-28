FROM golang:1.20-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /kotatsu-bot

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=build /kotatsu-bot /usr/local/bin/kotatsu-bot
WORKDIR /app
# Mount your config.yaml into /app/config.yaml when running the container.
VOLUME ["/app/config.yaml"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kotatsu-bot"]
