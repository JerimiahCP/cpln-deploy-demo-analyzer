FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy source and resolve dependencies at build time.
# go mod tidy downloads deps and generates go.sum inside the build layer.
# To cache deps across builds: commit go.sum after the first successful build,
# then split this into COPY go.mod go.sum ./ + RUN go mod download + COPY . .
COPY . .
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o analyzer .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/analyzer .
EXPOSE 8081
CMD ["./analyzer"]
