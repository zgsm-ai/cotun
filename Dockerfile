FROM golang:1.24.0 AS builder
WORKDIR /app
COPY . .

RUN go env -w CGO_ENABLED=0 && \
    go env -w GO111MODULE=on && \
    go env -w GOPROXY=https://goproxy.cn,https://mirrors.aliyun.com/goproxy,direct

RUN go mod tidy 
RUN go build -ldflags="-s -w -X 'github.com/zgsm-ai/share.BuildVersion=1.2.0'" -o cotun-server server-main/*.go
RUN chmod 755 cotun-server

FROM alpine:3.21 AS runtime
ENV env prod
ENV TZ Asia/Shanghai
WORKDIR /
COPY --from=builder /app/cotun-server /usr/local/bin/cotun-server
ENTRYPOINT ["/usr/local/bin/cotun-server"]
