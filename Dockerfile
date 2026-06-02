# syntax=docker/dockerfile:1

# Build Image
FROM golang:1.26.3 AS builder

WORKDIR /app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
COPY go.mod go.sum ./
RUN go mod download

RUN mkdir -p /app/bin

COPY . .
RUN CGO_ENABLED=0 go build -ldflags '-s -w -extldflags "-static"' -o /app/bin -v 

# Runtime Image
FROM gcr.io/distroless/static-debian13:nonroot

ARG REPO=tback/fritzbox_exporter

LABEL org.opencontainers.image.source https://github.com/${REPO}

ENV USERNAME username
ENV PASSWORD password
ENV GATEWAY_URL http://fritz.box:49000
ENV GATEWAY_LUAURL http://fritz.box
ENV LISTEN_ADDRESS 0.0.0.0:9042

USER nonroot

WORKDIR /

COPY --from=builder /app/bin/fritzbox_exporter /fritzbox_exporter
COPY metrics.json metrics-lua.json /

EXPOSE 9042

ENTRYPOINT [ "/fritzbox_exporter" ]
