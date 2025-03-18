# Use a minimal base image
FROM golang:1.23-alpine AS builder

# Set working directory
WORKDIR /app

# Copy go modules and install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the Go app
RUN go build -o influx-mqtt-homeassistant

# Use a small runtime image
FROM alpine:latest

# Install CA certificates for HTTPS (needed for InfluxDB connections) and tzdata for timezone handling
RUN apk add --no-cache ca-certificates tzdata

# Set timezone environment variable
ENV TZ=Pacific/Auckland

# Set working directory
WORKDIR /root/

# Copy compiled binary from builder
COPY --from=builder /app/influx-mqtt-homeassistant .

# Run the binary
CMD ["./influx-mqtt-homeassistant"]
