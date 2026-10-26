#!/bin/sh
# Build the site and put it live. Takes a few seconds.
#   ./deploy/deploy.sh root@your-server-ip
# For an ARM server (e.g. Hetzner CAX), run with: GOARCH=arm64 ./deploy/deploy.sh ...
set -eu
SERVER="${1:?usage: ./deploy/deploy.sh root@your-server-ip}"
cd "$(dirname "$0")/.."
GOOS=linux GOARCH="${GOARCH:-amd64}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o paxvelo-linux .
# Same installer GitHub uses: it restarts the site, and puts the old version back if the new one doesn't start.
scp paxvelo-linux "$SERVER:/home/deploy/paxvelo.new"
ssh "$SERVER" /usr/local/bin/paxvelo-install
rm -f paxvelo-linux
echo "Live."
