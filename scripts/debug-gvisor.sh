#!/bin/sh
# TEMPORARY: can Docker's embedded DNS work inside gVisor on the runner, with Docker on legacy iptables and --reproduce-nat?
set +e
echo "=== kernel and firewall"
uname -r; iptables --version; update-alternatives --display iptables 2>&1 | head -8
lsmod | grep -E "ip_tables|iptable_nat|nf_tables" | head
ls /usr/sbin/iptables-legacy /usr/sbin/xtables-legacy-multi 2>&1
echo "=== switch Docker to legacy iptables"
sudo modprobe ip_tables iptable_nat iptable_filter 2>&1
sudo update-alternatives --set iptables /usr/sbin/iptables-legacy 2>&1
sudo update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy 2>&1
iptables --version
sudo runsc install -- --reproduce-nat 2>&1 | tail -2
sudo jq '. + {"default-runtime": "runsc"}' /etc/docker/daemon.json | sudo tee /etc/docker/daemon.json.new >/dev/null && sudo mv /etc/docker/daemon.json.new /etc/docker/daemon.json
sudo systemctl restart docker; sleep 3
docker run --rm alpine:3 dmesg 2>&1 | head -3
docker network create dbg-open >/dev/null
docker network create --internal dbg-int >/dev/null
docker run -d --name dbg-a1 --network dbg-open --network-alias control alpine:3 sleep 300 >/dev/null
docker run -d --name dbg-a2 --network dbg-int --network-alias control alpine:3 sleep 300 >/dev/null
for net in dbg-open dbg-int; do
  echo "=== runsc on $net"
  docker run --rm --network $net alpine:3 sh -c 'nslookup control 2>&1 | tail -4; getent hosts control; echo "rc=$?"'
done
docker rm -f dbg-a1 dbg-a2 >/dev/null; docker network rm dbg-open dbg-int >/dev/null
