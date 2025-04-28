FROM golang:1.24-bookworm AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG VERSION="dev"
RUN apt-get update && apt-get install git
RUN go build -v -ldflags "-X main.version=${VERSION}" ./cmd/firecore

FROM ubuntu:24.10

RUN apt-get update && apt-get -y install ca-certificates htop iotop sysstat strace lsof curl jq tzdata

RUN mkdir -p /app/ && curl -Lo /app/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/v0.4.12/grpc_health_probe-linux-amd64 && chmod +x /app/grpc_health_probe

WORKDIR /app

COPY --from=build /app/firecore /app/firecore

ENV PATH="$PATH:/app"

COPY docker/motd /etc/motd
COPY docker/99-firehose-core.sh /etc/profile.d/
RUN echo ". /etc/profile.d/99-firehose-core.sh" > /root/.bash_aliases

ENTRYPOINT [ "/app/firecore" ]
