ARG GO_IMAGE=golang:1.25-alpine
ARG MEDIA_IMAGE=bluenviron/mediamtx:1.21.3
FROM ${MEDIA_IMAGE} AS media

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /playstream .

FROM alpine:3.23
COPY --from=media /mediamtx /usr/local/bin/mediamtx
COPY --from=media /LICENSE /usr/share/licenses/mediamtx/LICENSE
COPY --from=build /playstream /usr/local/bin/playstream
EXPOSE 53/udp 53/tcp 1935/tcp 8080/tcp
ENTRYPOINT ["/usr/local/bin/playstream"]
