#!/bin/bash
# TEMPORARY: bridge forwarding changes and Podman events on one timeline (/tmp/timeline.log).
declare -A last
sudo podman events --stream --format json | python3 -c '
import sys, json, datetime
for l in sys.stdin:
    e = json.loads(l)
    t = datetime.datetime.fromtimestamp(e.get("time", 0)).strftime("%H:%M:%S.%f")[:12]
    print(t, "EVENT", e.get("Type"), e.get("Status"), e.get("Name"), (e.get("Attributes") or {}).get("network", ""), flush=True)
' >> /tmp/timeline.log 2>&1 &
while true; do
	for b in $(ip -br link | awk '/^podman[0-9]/{print $1}'); do
		v=$(cat /proc/sys/net/ipv4/conf/$b/forwarding 2>/dev/null); a=$(ip -br addr show $b | awk '{print $3}' | cut -d/ -f1)
		if [ "${last[$b]}" != "$v $a" ]; then echo "$(date +%T.%N | cut -c1-12) BRIDGE $b $a forwarding=$v (was ${last[$b]:-new})" >> /tmp/timeline.log; last[$b]="$v $a"; echo "      networks: $(sudo podman network ls --format '{{.Name}}={{range .Subnets}}{{.Subnet}}{{end}}' | grep -v '^podman=' | sed 's/spec-[0-9a-f]*_//' | tr '\n' ' ')" >> /tmp/timeline.log; fi
	done
	for b in "${!last[@]}"; do ip link show $b >/dev/null 2>&1 || { echo "$(date +%T.%N | cut -c1-12) BRIDGE $b gone" >> /tmp/timeline.log; unset "last[$b]"; }; done
	sleep 0.05
done
