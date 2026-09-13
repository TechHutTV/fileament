FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
COPY --from=build /fileament /fileament
COPY --from=build --chown=65532:65532 /runtime/ /
USER 65532:65532
WORKDIR /
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/fileament"]
