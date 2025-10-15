# 1단계: Go 바이너리 빌드
FROM golang:1.24 AS builder

WORKDIR /app

# 모듈 다운로드
COPY go.mod ./
RUN go mod tidy

COPY go.mod go.sum ./
RUN go mod download

# 애플리케이션 소스 복사 및 빌드
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o webhook-server main.go

# 2단계: 런타임 이미지 구성
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

# TLS 인증서 마운트 위치 생성
RUN mkdir -p /tls
WORKDIR /

# 바이너리 복사
COPY --from=builder /app/webhook-server /webhook-server

# TLS 경로를 볼륨으로 노출
VOLUME /tls

CMD ["/webhook-server"]

EXPOSE 8443
