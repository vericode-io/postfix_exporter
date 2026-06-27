FROM golang:1.25 AS builder
WORKDIR /src

# avoid downloading the dependencies on succesive builds
RUN apt-get update -qq && apt-get install -qqy \
  build-essential \
  libsystemd-dev

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .

# Force the go compiler to use modules
ENV GO111MODULE=on
RUN go test -coverpkg=github.com/vericode-io/postfix_exporter .
RUN go build -o /bin/postfix_exporter

FROM debian:bookworm-slim
RUN useradd -m -s /bin/bash postfix_exporter
USER postfix_exporter
EXPOSE 9154
WORKDIR /home/postfix_exporter
COPY --from=builder /bin/postfix_exporter /bin/
ENTRYPOINT ["/bin/postfix_exporter"]
