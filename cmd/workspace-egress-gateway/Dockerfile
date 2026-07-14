FROM golang:1.25.0-bookworm@sha256:c4f7e4a02e718bdb76b37b68c8c0e487c99fd0c7b171b2c9f51e3c5e14ac287a AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/workspace-egress-gateway ./cmd/workspace-egress-gateway

FROM gcr.io/distroless/static-debian12:nonroot@sha256:87bce11be0af225e4ca761c40babb06d6d559f5767fbf7dc3c47f0f1a466b92c
COPY --from=build /out/workspace-egress-gateway /workspace-egress-gateway
USER 65532:65532
ENTRYPOINT ["/workspace-egress-gateway"]
