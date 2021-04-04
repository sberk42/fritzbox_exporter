FROM golang:rc-alpine3.13 AS builder
RUN go get github.com/sberk42/fritzbox_exporter/

FROM alpine:latest
ARG USERNAME PASSWORD GWURL
ENV USERNAME ${USERNAME} && \
PASSWORD ${PASSWORD} && \
GWURL ${GWURL}
WORKDIR /root/
COPY --from=builder /go/bin/fritzbox_exporter . 
COPY metrics.json metrics-lua.json ./
EXPOSE 9042
ENTRYPOINT ./fritzbox_exporter -gateway-url ${GWURL} -password ${PASSWORD} -username ${USERNAME} -listen-address 0.0.0.0:9042
