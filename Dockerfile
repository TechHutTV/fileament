FROM node:22-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/server/dist ./internal/server/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /fileament ./cmd/fileament

FROM gcr.io/distroless/static
COPY --from=build /fileament /fileament
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/fileament"]
