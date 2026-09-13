FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ENV PATH="/usr/local/go/bin:${PATH}" GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./cmd/fileament/dist
RUN CGO_ENABLED=0 go test -tags embedded_ui ./cmd/fileament
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedded_ui -ldflags="-s -w" -o /fileament ./cmd/fileament
RUN mkdir -p /runtime/data && chmod 0700 /runtime/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /fileament /fileament
COPY --from=build --chown=65532:65532 /runtime/ /
USER 65532:65532
WORKDIR /
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/fileament"]
