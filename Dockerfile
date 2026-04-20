# syntax=docker/dockerfile:1.23
# Multi-arch via BUILDPLATFORM; final image is built for $TARGETARCH (linux/arm64 in-cluster).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/funnel-manager ./

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/funnel-manager /funnel-manager
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/funnel-manager"]
