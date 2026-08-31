# Етап збірки
FROM golang:1.22-alpine AS builder

# Встановлюємо залежності для Cgo (SQLite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Копіюємо файли конфігурації модулів та сирцевий код
COPY go.mod go.su[m] ./
COPY . .

# Автоматично створюємо/оновлюємо go.sum і завантажуємо модулі всередині Docker
RUN go mod tidy
RUN go mod download

# Вмикаємо CGO для коректної роботи mattn/go-sqlite3
ENV CGO_ENABLED=1
ENV GOOS=linux

RUN go build -o main .

# Фінальний мінімальний образ
FROM alpine:latest

RUN apk add --no-cache ca-certificates sqlite-libs tzdata

WORKDIR /app

RUN mkdir -p /app/data

COPY --from=builder /app/main .
COPY --from=builder /app/static ./static

EXPOSE 8085

CMD ["./main"]
