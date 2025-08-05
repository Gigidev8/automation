# Stage 1: Build the Go application
FROM golang:1.19-alpine as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /server .

# Stage 2: Create the final, smaller image
FROM alpine:latest

WORKDIR /app

COPY --from=builder /server /server

# Expose the port the server runs on
EXPOSE 8080

# Run the server
CMD ["/server"]
