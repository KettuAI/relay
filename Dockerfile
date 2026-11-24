# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/kettu-relay ./cmd/kettu-relay
RUN mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/kettu-relay /usr/local/bin/kettu-relay
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV KETTU_RELAY_DATA=/data
VOLUME /data
EXPOSE 443 80
ENTRYPOINT ["/usr/local/bin/kettu-relay"]
