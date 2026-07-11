# Сборка из корня модуля sorm:
#   docker build -f examples/webapp/Dockerfile .
# (compose.yaml делает это сам: context = ../..)

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/webapp ./examples/webapp

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/webapp /app/webapp
COPY examples/webapp/migrations /app/migrations
ENV MIGRATIONS_DIR=/app/migrations
EXPOSE 8080
ENTRYPOINT ["/app/webapp"]
