FROM golang:1.25-bookworm@sha256:a9c020ee3d1508c7be5435c262434e3d3fc1d0e76a11afeb9ddae7d60bc86aa4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/workspace-agent ./cmd/workspace-agent

FROM gcr.io/distroless/static-debian12:nonroot@sha256:b7bb25d9f7c31d2bdd1982feb4dafcaf137703c7075dbe2febb41c24212b946f
COPY --from=build /out/workspace-agent /workspace-agent
USER nonroot:nonroot
ENTRYPOINT ["/workspace-agent"]
