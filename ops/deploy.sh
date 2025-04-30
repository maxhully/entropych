#!/usr/bin/env bash

# Prior art:
# https://gist.github.com/WesleyAC/b3aaa0292579158ad566c140415c875d

set -euxo pipefail

server_ssh=$1

mkdir -p ./build/

go test ./...

# TODO: maybe I should just push the files to the server and compile it there
docker container rm entropych_build || true
docker build --platform=linux/amd64 -f ops/entropych.Dockerfile -t entropych_build .
docker container create --platform=linux/amd64 --name=entropych_build entropych_build
docker container cp entropych_build:/go/src/entropych/server ./build/server
docker container cp entropych_build:/go/src/entropych/bots ./build/bots
docker container rm entropych_build

ssh -q -T "$server_ssh" <<EOL
id -u entropych >/dev/null 2>&1 || useradd -m entropych
mkdir -p /etc/entropych
mkdir -p /home/entropych/versions
EOL

remote_binary_version="/home/entropych/versions/server-$(date +"%Y%m%d_%H%M%S")-$(git rev-parse --short HEAD)"

# TODO: only do this if it changed, so systemd doesn't ask for a daemon-reload
scp ops/entropych.service "$server_ssh:/etc/systemd/system/entropych.service"
scp ops/entropych.env "$server_ssh:/etc/entropych/entropych.env"

scp build/server "$server_ssh:$remote_binary_version"
# TODO: embed this in the binary, so that it's deployed as part of the single step below
rsync -rv --progress --exclude=".*\.DS_Store" ./static/ "$server_ssh:/home/entropych/static"
# shellcheck disable=SC2087
ssh -q -T "$server_ssh" <<EOL
	nohup sh -c "\
        rm "/home/entropych/server" && \
        ln -s "$remote_binary_version" "/home/entropych/server" && \
        systemctl restart entropych"
EOL
