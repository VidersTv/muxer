FROM golang:1.17.6 as builder

WORKDIR /tmp/muxer

COPY . .

ARG BUILDER
ARG VERSION

ENV MUXER_BUILDER=${BUILDER}
ENV MUXER_VERSION=${VERSION}

RUN apt-get update && apt-get install make git gcc -y && \
    make build_deps && \
    make

FROM alpine:latest

WORKDIR /app

COPY --from=builder /tmp/muxer/bin/muxer .

CMD ["/app/muxer"]
