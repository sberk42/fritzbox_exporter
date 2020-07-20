FROM golang:1.14.5 AS build-env

LABEL maintainer="Max Schmitt <max@schmitt.mx>"
LABEL maintainer="Andreas Schmid <service@aaschid.de>"
LABEL description="FRITZ!Box Prometheus exporter"

RUN go get -v github.com/sberk24/fritzbox_exporter && \
    cd /go/src/github.com/sberk24/fritzbox_exporter && \
    CGO_ENABLED=0 go build -v -o /exporter


FROM alpine

RUN apk update && apk add ca-certificates

COPY --from=build-env /go/src/github.com/sberk24/fritzbox_exporter/metrics.json /metrics.json
COPY --from=build-env /exporter /

EXPOSE 9133

ENTRYPOINT ["/exporter"]
CMD ["-listen-address", ":9133", "-metrics-file", "/metrics.json"]
