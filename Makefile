# The binary embeds the web frontend, so build the frontend first.
.PHONY: build web test dev

build: web
	go build -o trackside ./cmd/trackside

web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci
	touch web/node_modules

test:
	go test ./...
	cd web && npx tsc -b

# Frontend with hot reload on :5173, proxying the API to a server on :8080.
dev: web/node_modules
	cd web && npm run dev
