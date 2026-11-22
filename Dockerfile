FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -tags netgo -trimpath -ldflags="-s -w" -o /server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build --chown=app:app /server /app/server
USER app
EXPOSE 8080
ENV PORT=8080
ENTRYPOINT ["/app/server"]
