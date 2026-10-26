#!/bin/sh
# Turns off password logins over SSH, so only keys work. Run as root, after you
# have logged in with your key at least once:
#   scp deploy/lock-ssh.sh root@SERVER:/root/ && ssh root@SERVER 'sh lock-ssh.sh'
# It refuses to run if root has no key, since that would lock you out. Keep your
# current session open and check that a second login works before closing it.
set -eu
if ! grep -qE '^(ssh-|ecdsa-)' /root/.ssh/authorized_keys 2>/dev/null; then
	echo "root has no SSH key yet, so passwords stay on. Add your key first (ssh-copy-id root@THIS_SERVER)."
	exit 1
fi
mkdir -p /root/sshd-backup && cp -a /etc/ssh/sshd_config.d/. /root/sshd-backup/
printf 'PasswordAuthentication no\n' > /etc/ssh/sshd_config.d/50-cloud-init.conf
printf 'PermitRootLogin prohibit-password\nPasswordAuthentication no\n' > /etc/ssh/sshd_config.d/99-ctrl.conf
sshd -t
systemctl reload ssh
echo "Done: only SSH keys can log in now. The old settings are in /root/sshd-backup."
