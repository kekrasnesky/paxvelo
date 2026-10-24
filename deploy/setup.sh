#!/bin/sh
# One-time setup for a fresh Ubuntu 24.04 server. Run as root from this folder:
#   scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'
set -eu
apt-get update
apt-get install -y caddy unattended-upgrades   # security updates install themselves
id paxvelo >/dev/null 2>&1 || useradd --system --home /opt/paxvelo --shell /usr/sbin/nologin paxvelo
mkdir -p /opt/paxvelo/data /opt/paxvelo/backups
chown -R paxvelo:paxvelo /opt/paxvelo
cp paxvelo.service /etc/systemd/system/paxvelo.service
cp Caddyfile /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable paxvelo
systemctl restart caddy
# Nightly backup of the data file at 3am, keeping 30 days.
cat > /etc/cron.d/paxvelo-backup <<'CRON'
0 3 * * * paxvelo cp /opt/paxvelo/data/paxvelo.json /opt/paxvelo/backups/paxvelo-$(date +\%F).json && find /opt/paxvelo/backups -name '*.json' -mtime +30 -delete
CRON
echo
echo "Server ready. Next:"
echo "  1. Copy your paxvelo.conf to /opt/paxvelo/paxvelo.conf (then: chmod 600, chown paxvelo)"
echo "  2. From your computer, run: ./deploy/deploy.sh root@THIS_SERVER"
