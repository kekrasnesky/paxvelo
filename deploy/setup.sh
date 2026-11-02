#!/bin/sh
# One-time setup for a fresh Ubuntu 24.04 server. Run as root from this folder:
#   scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'
set -eu
apt-get update
apt-get install -y caddy curl unattended-upgrades rclone   # security updates install themselves
# LibreOffice without a screen, for turning Word minutes into PDFs (docpdf.go),
# and fonts that match Word's (Calibri, Cambria, Arial, Times New Roman).
apt-get install -y --no-install-recommends libreoffice-writer-nogui fonts-crosextra-carlito fonts-crosextra-caladea fonts-liberation
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
install -m 755 paxvelo-offsite /usr/local/bin/paxvelo-offsite
# Restart by itself at 4am when a security update needs it (a new kernel only runs after a reboot).
cat > /etc/apt/apt.conf.d/52paxvelo-reboot <<'APT'
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
APT
echo 'deploy ALL=(root) NOPASSWD: /usr/local/bin/paxvelo-install' > /etc/sudoers.d/paxvelo-deploy
chmod 440 /etc/sudoers.d/paxvelo-deploy
# Nightly backup of the data file at 3am, keeping 30 days, and a weekly copy
# of the newsletter photos and minutes files uploaded in the admin, keeping 8 weeks.
mkdir -p /opt/paxvelo/data/media /opt/paxvelo/data/files
chown paxvelo:paxvelo /opt/paxvelo/data/media /opt/paxvelo/data/files
cat > /etc/cron.d/paxvelo-backup <<'CRON'
0 3 * * * paxvelo cp /opt/paxvelo/data/paxvelo.json /opt/paxvelo/backups/paxvelo-$(date +\%F).json && find /opt/paxvelo/backups -name '*.json' -mtime +30 -delete
30 3 * * 0 paxvelo tar -czf /opt/paxvelo/backups/media-$(date +\%F).tgz -C /opt/paxvelo/data media files && find /opt/paxvelo/backups -name 'media-*.tgz' -mtime +56 -delete
CRON
# At 3:30am, copy those backups to Google Drive (does nothing until Drive is connected).
echo '30 3 * * * paxvelo /usr/local/bin/paxvelo-offsite' > /etc/cron.d/paxvelo-offsite
chmod 644 /etc/cron.d/paxvelo-offsite
echo
echo "Server ready. Next:"
echo "  1. Copy your paxvelo.conf to /opt/paxvelo/paxvelo.conf (then: chmod 600, chown paxvelo)"
echo "  2. Connect GitHub for automatic publishing (CLAUDE.md, Deploy),"
echo "     or publish by hand from your computer: ./deploy/deploy.sh root@THIS_SERVER"
echo "  3. Connect Google Drive for off-server backups (CLAUDE.md, Backups)"
echo "  4. Once your SSH key works, turn off password logins: sh lock-ssh.sh (CLAUDE.md, Deploy)"
