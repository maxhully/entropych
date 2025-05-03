#!/usr/bin/env bash

# Prior art:
# https://gist.github.com/WesleyAC/b3aaa0292579158ad566c140415c875d

set -euxo pipefail

server_ssh=$1

if ! test -f "./build/server"; then
    echo "Run ops/build.sh first to build linux binaries."
    exit 1
fi

ssh -q -T "$server_ssh" <<EOL
id -u entropych >/dev/null 2>&1 || useradd -m entropych
mkdir -p /etc/entropych
mkdir -p /home/entropych/versions && chown -R entropych:entropych /home/entropych
EOL

remote_binary_version="/home/entropych/versions/server-$(date +"%Y%m%d_%H%M%S")-$(git rev-parse --short HEAD)"

rsync --progress --checksum ops/entropych.service "$server_ssh:/etc/systemd/system/entropych.service"
rsync --progress --checksum ops/entropych.env "$server_ssh:/etc/entropych/entropych.env"

rsync --progress build/server "$server_ssh:$remote_binary_version"
rsync --progress build/bots "$server_ssh:/home/entropych/bots"
# TODO: embed this in the binary, so that it's deployed as part of the single step below
rsync -rv --progress --exclude=".DS_Store" ./static/ "$server_ssh:/home/entropych/static"
# shellcheck disable=SC2087
ssh -q -T "$server_ssh" <<EOL
    nohup sh -c "\
    rm "/home/entropych/server" && \
    ln -s "$remote_binary_version" "/home/entropych/server" && \
    systemctl restart entropych"
EOL
