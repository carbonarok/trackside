FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /trackside ./cmd/trackside

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /trackside /usr/local/bin/trackside
USER nobody
EXPOSE 8080
ENTRYPOINT ["trackside"]
CMD ["serve"]
