#!/bin/sh
# A throwaway ssh server for the specs: key authentication only, for the unprivileged user `ops`.
# It makes its own host key and its own client key at start; the specs read them back with `exec`.
set -eu

ssh-keygen -A
install -d -m 700 -o ops -g ops /home/ops/.ssh
ssh-keygen -q -t ed25519 -N '' -C ops@spec -f /home/ops/.ssh/id_ed25519
cp /home/ops/.ssh/id_ed25519.pub /home/ops/.ssh/authorized_keys
chown -R ops:ops /home/ops/.ssh
chmod 600 /home/ops/.ssh/authorized_keys

cat > /etc/ssh/sshd_config <<CONF
Port 22
HostKey /etc/ssh/ssh_host_ed25519_key
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowUsers ops
Subsystem sftp internal-sftp
CONF

# -e: log to stderr, which is how the specs know it is listening.
exec /usr/sbin/sshd -D -e
