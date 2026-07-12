# Build from the repository root: the example lives inside the sorm module.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /chat ./examples/chat/cmd/chat

FROM alpine:3.21
COPY --from=build /chat /chat
COPY examples/chat/appsettings.yaml /appsettings.yaml
EXPOSE 8080
ENTRYPOINT ["/chat"]
