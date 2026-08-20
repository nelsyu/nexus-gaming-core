FROM golang:alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
RUN go build -o bin/server.exe cmd/server/main.go

# Run stage
FROM alpine:latest

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/bin/server.exe .

# Expose port
EXPOSE 8080

# Command to run the executable
CMD ["./server.exe"]
