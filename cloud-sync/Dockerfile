# syntax=docker/dockerfile:1.7

FROM golang:1.22-alpine AS build
WORKDIR /src
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/cloud-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cloud-sync /usr/local/bin/cloud-sync
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/cloud-sync"]
