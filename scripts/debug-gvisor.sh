#!/bin/sh
# TEMPORARY: what Docker's embedded DNS does inside gVisor on the runner
set +e
docker network create dbg-open >/dev/null
docker network create --internal dbg-int >/dev/null
docker run -d --name dbg-a1 --network dbg-open --network-alias control alpine:3 sleep 300 >/dev/null
docker run -d --name dbg-a2 --network dbg-int --network-alias control alpine:3 sleep 300 >/dev/null
for net in dbg-open dbg-int; do
  echo "=== runsc on $net"
  docker run --rm --network $net alpine:3 sh -c 'cat /etc/resolv.conf | grep -v "^#"; ip addr show eth0 | grep inet; echo "-- nslookup control"; nslookup control 2>&1 | tail -4; echo "-- nslookup control 127.0.0.11"; nslookup control 127.0.0.11 2>&1 | tail -4; echo "-- getent"; getent hosts control; echo "rc=$?"; echo "-- ping by name"; ping -c1 -W2 control 2>&1 | tail -2'
  echo "=== runc on $net"
  docker run --rm --runtime runc --network $net alpine:3 sh -c 'nslookup control 2>&1 | tail -4; getent hosts control; echo "rc=$?"'
done
echo "=== nft ruleset of the host (docker nat)"
sudo nft list ruleset 2>&1 | grep -n "127.0.0.11" | head
echo "=== the runtime config"
docker info --format '{{.DefaultRuntime}}'; cat /etc/docker/daemon.json
docker rm -f dbg-a1 dbg-a2 >/dev/null; docker network rm dbg-open dbg-int >/dev/null
