FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /gotems ./cmd/gotems

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /gotems /usr/local/bin/gotems
COPY configs/gotems.yaml /etc/gotems/gotems.yaml

EXPOSE 8080
ENTRYPOINT ["gotems"]
CMD ["serve", "--addr", ":8080"]
