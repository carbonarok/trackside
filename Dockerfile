FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /trackside ./cmd/trackside

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /trackside /usr/local/bin/trackside
USER nobody
EXPOSE 8080
ENTRYPOINT ["trackside"]
CMD ["serve"]
