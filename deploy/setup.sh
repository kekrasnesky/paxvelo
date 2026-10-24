#!/bin/sh
# One-time setup for a fresh Ubuntu 24.04 server. Run as root from this folder:
#   scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'
set -eu
apt-get update
apt-get install -y caddy curl unattended-upgrades   # security updates install themselves
id paxvelo >/dev/null 2>&1 || useradd --system --home /opt/paxvelo --shell /usr/sbin/nologin paxvelo
mkdir -p /opt/paxvelo/data /opt/paxvelo/backups
chown -R paxvelo:paxvelo /opt/paxvelo
cp paxvelo.service /etc/systemd/system/paxvelo.service
cp Caddyfile /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable paxvelo
systemctl restart caddy
# A "deploy" user that GitHub uses to publish changes. All it can do is upload a
# new build and run paxvelo-install. Its key goes in ~deploy/.ssh/authorized_keys.
id deploy >/dev/null 2>&1 || useradd --create-home --shell /bin/sh deploy
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys
chmod 600 /home/deploy/.ssh/authorized_keys
install -m 755 paxvelo-install /usr/local/bin/paxvelo-install
echo 'deploy ALL=(root) NOPASSWD: /usr/local/bin/paxvelo-install' > /etc/sudoers.d/paxvelo-deploy
chmod 440 /etc/sudoers.d/paxvelo-deploy
# Nightly backup of the data file at 3am, keeping 30 days, and a weekly copy
# of the newsletter photos uploaded in the admin, keeping 8 weeks.
cat > /etc/cron.d/paxvelo-backup <<'CRON'
0 3 * * * paxvelo cp /opt/paxvelo/data/paxvelo.json /opt/paxvelo/backups/paxvelo-$(date +\%F).json && find /opt/paxvelo/backups -name '*.json' -mtime +30 -delete
30 3 * * 0 paxvelo [ -d /opt/paxvelo/data/media ] && tar -czf /opt/paxvelo/backups/media-$(date +\%F).tgz -C /opt/paxvelo/data media && find /opt/paxvelo/backups -name 'media-*.tgz' -mtime +56 -delete
CRON
echo
echo "Server ready. Next:"
echo "  1. Copy your paxvelo.conf to /opt/paxvelo/paxvelo.conf (then: chmod 600, chown paxvelo)"
echo "  2. Connect GitHub for automatic publishing (README.md, Automatic publishing),"
echo "     or publish by hand from your computer: ./deploy/deploy.sh root@THIS_SERVER"
