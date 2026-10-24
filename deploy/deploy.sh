#!/bin/sh
# Build the site and put it live. Takes a few seconds.
#   ./deploy/deploy.sh root@your-server-ip
# For an ARM server (e.g. Hetzner CAX), run with: GOARCH=arm64 ./deploy/deploy.sh ...
set -eu
SERVER="${1:?usage: ./deploy/deploy.sh root@your-server-ip}"
cd "$(dirname "$0")/.."
GOOS=linux GOARCH="${GOARCH:-amd64}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o paxvelo-linux .
scp paxvelo-linux "$SERVER:/opt/paxvelo/paxvelo.new"
ssh "$SERVER" 'chmod 755 /opt/paxvelo/paxvelo.new && mv /opt/paxvelo/paxvelo.new /opt/paxvelo/paxvelo && systemctl restart paxvelo && sleep 1 && systemctl is-active paxvelo'
rm -f paxvelo-linux
echo "Live."
