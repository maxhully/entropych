#!/usr/bin/env bash

set -euxo pipefail

mkdir -p ./build/

go test ./...

# TODO: maybe I should just push the files to the server and compile it there
docker container rm entropych_build || true
docker build --platform=linux/amd64 -f ops/entropych.Dockerfile -t entropych_build .
docker container create --platform=linux/amd64 --name=entropych_build entropych_build
docker container cp entropych_build:/go/src/entropych/server ./build/server
docker container cp entropych_build:/go/src/entropych/bots ./build/bots
docker container cp entropych_build:/go/src/entropych/backfill_avatars ./build/backfill_avatars
docker container rm entropych_build
