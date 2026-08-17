.PHONY: all build run test clean db-up db-down

all: build

build:
	go build -o bin/server.exe cmd/server/main.go

run:
	go run cmd/server/main.go

test:
	go test -v ./...

clean:
	rm -rf bin/

# Docker Compose 指令
db-up:
	docker-compose up -d --build

db-down:
	docker-compose down
