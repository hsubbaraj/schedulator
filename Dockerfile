FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /schedulator ./cmd/schedulator

FROM alpine:3.19

RUN adduser -D -u 1000 schedulator
USER schedulator

COPY --from=builder /schedulator /usr/local/bin/schedulator

EXPOSE 8080

ENTRYPOINT ["schedulator"]
