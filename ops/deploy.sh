#!/usr/bin/env bash

set -euxo pipefail

server_ssh=$1

mkdir -p ./build/

go test ./...

docker build --platform=linux/amd64 -f ops/entropych.Dockerfile -t entropych_build .
docker container create --platform=linux/amd64 --name=entropych_build entropych_build
docker container cp entropych_build:/go/src/entropych/server ./build/server
docker container rm entropych_build

ssh -q -T "$server_ssh" <<EOL
id -u entropych >/dev/null 2>&1 || useradd -m entropych
mkdir -p /etc/entropych
EOL

scp build/server "$server_ssh:/home/entropych/"

scp ops/entropych.service "$server_ssh:/etc/systemd/system/entropych.service"
scp ops/entropych.env "$server_ssh:/etc/entropych/entropych.env"

