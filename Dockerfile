FROM golang:1.21-alpine as build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN go build ./cmd/firecore

####

FROM alpine:edge

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=build /app/firecore /app/firecore

ENTRYPOINT [ "/app/firecore" ]