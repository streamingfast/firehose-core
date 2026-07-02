FROM golang:1.26-bookworm AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG VERSION="dev"
RUN apt-get update && apt-get -y install git
RUN go build -v -ldflags "-X main.version=${VERSION}" ./cmd/firecore

FROM ubuntu:24.04

ARG TARGETPLATFORM

# gettext-base is needed for envsubst
RUN apt-get update && apt-get -y upgrade && apt-get -y install ca-certificates htop iotop sysstat strace lsof curl jq tzdata file gettext-base && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /app/firecore /app/firecore

ENV PATH="$PATH:/app"

COPY docker/motd /etc/motd
COPY docker/motd_reader /etc/motd_reader
COPY docker/99-firehose-core.sh /etc/profile.d/
COPY docker/scripts/ /app/
RUN chmod +x /app/reader-*
RUN echo ". /etc/profile.d/99-firehose-core.sh" > /root/.bash_aliases

ENTRYPOINT [ "/app/firecore" ]
