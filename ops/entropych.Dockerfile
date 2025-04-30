FROM golang:1.24
WORKDIR /go/src/entropych

COPY go.mod go.sum ./
RUN go mod download

# TODO: make this depend on less context?
COPY . .
RUN GOOS=linux GOARCH=amd64 go build ./cmd/server
RUN GOOS=linux GOARCH=amd64 go build ./cmd/bots
