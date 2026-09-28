ARG GO_BUILD_IMAGE=golang:1.23
ARG GOPROXY=https://proxy.golang.org,direct
FROM ${GO_BUILD_IMAGE} AS build
ARG GOPROXY

WORKDIR /src
ENV GOPROXY=${GOPROXY}
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/media-dock ./cmd/media-dock
RUN test -s /etc/ssl/certs/ca-certificates.crt \
    && test -f /usr/share/zoneinfo/Asia/Shanghai \
    && mkdir -p /out/rootfs/etc/ssl/certs /out/rootfs/usr/share/zoneinfo /out/rootfs/tmp \
    && cp /etc/ssl/certs/ca-certificates.crt /out/rootfs/etc/ssl/certs/ca-certificates.crt \
    && cp -a /usr/share/zoneinfo/. /out/rootfs/usr/share/zoneinfo/ \
    && printf 'mediadock:x:1000:1000:MediaDock:/nonexistent:/sbin/nologin\n' > /out/rootfs/etc/passwd \
    && printf 'mediadock:x:1000:\n' > /out/rootfs/etc/group \
    && chmod 1777 /out/rootfs/tmp

FROM scratch
ENV TZ=Asia/Shanghai
COPY --from=build /out/media-dock /media-dock
COPY --from=build /out/rootfs /
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/media-dock"]
