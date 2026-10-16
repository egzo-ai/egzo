#!/bin/sh
# Prepare a fresh CI runner to be one reference platform of the specs (specs/README.md). The runner is a throwaway VM, so it
# is changed freely: nothing here is meant for a developer machine.
#
#   scripts/ci-engine.sh docker | docker-gvisor | podman | podman-rootless
#
# Docker and Podman come with the runner image; this widens Docker's network pool, registers gVisor with Docker and starts the
# Podman API socket the platform names. For podman-rootless the socket's directory is exported through $GITHUB_ENV, as the specs look for it there.
set -eu

platform=${1:?usage: ci-engine.sh docker|docker-gvisor|podman|podman-rootless}

# Docker gives each project and each instance a network of its own out of a default pool that holds about 30 subnets, and the
# specs run in several workers at once (see specs.yml): "all predefined address pools have been fully subnetted". Give Docker a
# pool of 2048 /24 networks, from a range the runner does not use (its own address is printed to check), and merge it into
# whatever /etc/docker/daemon.json holds (gVisor's runtime is added to the same file below).
docker_networks() {
	ip -4 route show | sed 's/^/runner route: /'
	sudo mkdir -p /etc/docker
	[ -s /etc/docker/daemon.json ] || echo '{}' | sudo tee /etc/docker/daemon.json >/dev/null
	sudo jq '. + {"default-address-pools": [{"base": "10.200.0.0/13", "size": 24}]}' /etc/docker/daemon.json | sudo tee /etc/docker/daemon.json.new >/dev/null
	sudo mv /etc/docker/daemon.json.new /etc/docker/daemon.json
	sudo systemctl restart docker
	docker info >/dev/null
	docker network create ci-pool-check >/dev/null
	docker network inspect ci-pool-check --format 'new networks come from {{(index .IPAM.Config 0).Subnet}}'
	docker network rm ci-pool-check >/dev/null
}

case "$platform" in
docker)
	docker_networks
	docker version
	;;
docker-gvisor)
	docker_networks
	# https://gvisor.dev/docs/user_guide/install/ : one archive per architecture, checked against its published sum, with
	# runsc and its containerd shim somewhere inside
	case $(uname -m) in aarch64 | arm64) arch=aarch64 ;; *) arch=x86_64 ;; esac
	url=https://storage.googleapis.com/gvisor/releases/release/latest/$arch
	tmp=$(mktemp -d)
	(
		cd "$tmp"
		curl -fsSL -O "$url/gvisor.tar.bz2" -O "$url/gvisor.tar.bz2.sha512"
		sha512sum -c gvisor.tar.bz2.sha512
		mkdir x
		tar -xjf gvisor.tar.bz2 -C x
		find x -type f | head -20
		for binary in runsc containerd-shim-runsc-v1; do
			found=$(find x -type f -name "$binary" | head -1)
			[ -n "$found" ] || { echo "$binary is not in the gVisor archive" >&2; exit 1; }
			sudo install -m 0755 "$found" /usr/local/bin/"$binary"
		done
	)
	rm -rf "$tmp"
	# registers the runtime `runsc` in /etc/docker/daemon.json
	sudo runsc install
	sudo systemctl restart docker
	runsc --version
	docker info --format '{{json .Runtimes}}'
	docker run --rm --runtime=runsc alpine:3 dmesg | head -3
	;;
podman)
	# rootful Podman: the system socket, readable by the runner user (the VM is thrown away)
	sudo systemctl enable --now podman.socket
	sudo chmod 0666 /run/podman/podman.sock
	sudo podman version
	;;
podman-rootless)
	# rootless Podman: a user socket in $XDG_RUNTIME_DIR/podman/podman.sock, served by `podman system service`
	runtime=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
	sudo mkdir -p "$runtime"
	sudo chown "$(id -u):$(id -g)" "$runtime"
	chmod 0700 "$runtime"
	mkdir -p "$runtime/podman"
	nohup podman system service --time=0 "unix://$runtime/podman/podman.sock" >"$runtime/podman/service.log" 2>&1 &
	for _ in $(seq 1 30); do [ -S "$runtime/podman/podman.sock" ] && break; sleep 1; done
	[ -S "$runtime/podman/podman.sock" ] || { cat "$runtime/podman/service.log"; echo "the Podman user socket did not appear" >&2; exit 1; }
	[ -z "${GITHUB_ENV:-}" ] || echo "XDG_RUNTIME_DIR=$runtime" >>"$GITHUB_ENV"
	podman version
	;;
*)
	echo "ci-engine.sh: unknown platform $platform" >&2
	exit 2
	;;
esac
