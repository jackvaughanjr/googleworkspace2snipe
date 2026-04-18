FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /app/googleworkspace2snipe .

FROM alpine:3.21
COPY --from=builder /app/googleworkspace2snipe /app/googleworkspace2snipe
ENTRYPOINT ["/app/googleworkspace2snipe"]
