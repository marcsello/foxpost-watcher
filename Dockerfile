FROM golang:1.21-alpine3.18 as builder

COPY . /src/
WORKDIR /src

RUN apk add --no-cache make=4.4.1-r1 && make -j "$(nproc)"

FROM alpine:3.18

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates

COPY --from=builder /src/main /app/main

USER 1000
ENTRYPOINT [ "/app/main" ]
