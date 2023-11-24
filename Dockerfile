FROM golang:1.21-alpine as build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN go build ./cmd/firecore

####

FROM alpine:edge


RUN apk --no-cache add \
        ca-certificates htop iotop sysstat \
        strace lsof curl jq tzdata

RUN mkdir -p /app/ && curl -Lo /app/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/v0.4.12/grpc_health_probe-linux-amd64 && chmod +x /app/grpc_health_probe

WORKDIR /app

COPY --from=build /app/firecore /app/firecore

ENTRYPOINT [ "/app/firecore" ]