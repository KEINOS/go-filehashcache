FROM golang:1.27-bookworm AS test

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build ./...
RUN go test -race -cover ./...
RUN go -C _examples/basic test -race ./...
