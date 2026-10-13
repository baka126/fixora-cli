FROM golang:1.26.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/serviceprobe ./internal/serviceprobe
COPY cmd/fixora-service-probe ./cmd/fixora-service-probe
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /fixora-service-probe ./cmd/fixora-service-probe
FROM scratch
COPY --from=build /fixora-service-probe /fixora-service-probe
USER 65532:65532
ENTRYPOINT ["/fixora-service-probe"]
