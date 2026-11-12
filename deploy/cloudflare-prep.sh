#!/bin/sh
# Gets the server ready to sit behind Cloudflare's CDN, without switching it
# on: upgrades Caddy to its official package (Ubuntu's is 2.6, too old for
# Cloudflare's visitor-address header) and installs deploy/Caddyfile, which
# trusts that header only from Cloudflare's addresses. Safe to run before or
# after the DNS record is set to "Proxied". Run as root from the deploy folder:
#   scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh cloudflare-prep.sh'
set -eu
cd "$(dirname "$0")"
export DEBIAN_FRONTEND=noninteractive

backup=/root/Caddyfile.before-cloudflare-$(date +%F-%H%M%S)
cp -a /etc/caddy/Caddyfile "$backup"
echo "Saved the current Caddyfile as $backup"

# Caddy's own package source (https://caddyserver.com/docs/install#debian-ubuntu-raspbian)
if [ ! -f /etc/apt/sources.list.d/caddy-stable.list ]; then
	apt-get install -y -q debian-keyring debian-archive-keyring apt-transport-https gnupg curl
	curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --batch --yes --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
	curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
fi
apt-get update -q
# Keep the Caddyfile that's there while upgrading; the new one goes in below.
apt-get install -y -q -o Dpkg::Options::=--force-confold caddy
caddy version

# Check the new Caddyfile before it replaces the old one.
caddy validate --config ./Caddyfile --adapter caddyfile
cp ./Caddyfile /etc/caddy/Caddyfile
systemctl reload caddy
sleep 2
if curl -fsS -o /dev/null https://www.paxvelo.club/; then
	echo "Done: Caddy $(caddy version | cut -d' ' -f1) is serving the site with the Cloudflare settings."
else
	echo "The site didn't answer: putting the old Caddyfile back."
	cp -a "$backup" /etc/caddy/Caddyfile
	systemctl reload caddy
	exit 1
fi
