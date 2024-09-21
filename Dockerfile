# Stage 1: Build the Go application
FROM golang:1.23-alpine AS builder

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN go build ./cmd/pgsql-prometheus-exporter

# Stage 2: Create a lightweight image for the Go app
FROM alpine:latest

# Set up working directory
WORKDIR /app

# Copy the Go binary from the build stage
COPY --from=builder /app/pgsql-prometheus-exporter .

# Expose the necessary port (if needed)
EXPOSE 9042

# Command to run the application
CMD ["./pgsql-prometheus-exporter"]