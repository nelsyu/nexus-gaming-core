.PHONY: all build run test clean db-up db-down test-load

all: build

build:
	go build -o bin/server.exe cmd/server/main.go

run:
	docker-compose up --build -d api-server
	docker-compose logs -f api-server

test:
	go test -v ./...

clean:
	rm -rf bin/

# Docker Compose 指令
db-up:
	docker-compose up -d --build

db-down:
	docker-compose down

# k6 壓力測試指令 (透過 Docker 執行，無需本機安裝)
test-load:
	docker run --rm -i --network nexus-gaming-core_default grafana/k6 run -e API_URL=http://api-server:8080 - < tests/load/spin.js
