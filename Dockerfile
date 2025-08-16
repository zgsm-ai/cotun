FROM golang:1.24.0 AS builder
WORKDIR /app
COPY . .

RUN go env -w CGO_ENABLED=0 && \
    go env -w GO111MODULE=on && \
    go env -w GOPROXY=https://goproxy.cn,https://mirrors.aliyun.com/goproxy,direct

RUN go mod tidy 
RUN go build -ldflags="-s -w" -o cotun *.go
RUN chmod 755 cotun

FROM alpine:3.21 AS runtime
ENV env prod
ENV TZ Asia/Shanghai
WORKDIR /
COPY --from=builder /app/cotun /usr/local/bin/cotun
ENTRYPOINT ["/usr/local/bin/cotun"]
