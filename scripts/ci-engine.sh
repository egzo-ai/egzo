#!/bin/sh
# Prepare a fresh CI runner to be one reference platform of the specs (specs/README.md). The runner is a throwaway VM, so it
# is changed freely: nothing here is meant for a developer machine.
#
#   scripts/ci-engine.sh docker | docker-gvisor | podman | podman-rootless
#
# Docker and Podman come with the runner image; this registers gVisor with Docker and starts the Podman API socket the
# platform names. For podman-rootless the socket's directory is exported through $GITHUB_ENV, as the specs look for it there.
set -eu

platform=${1:?usage: ci-engine.sh docker|docker-gvisor|podman|podman-rootless}

case "$platform" in
docker)
	docker version
	;;
docker-gvisor)
	# https://gvisor.dev/docs/user_guide/install/ : the binaries are checked against the published sums
	arch=$(uname -m)
	url=https://storage.googleapis.com/gvisor/releases/release/latest/$arch
	tmp=$(mktemp -d)
	(
		cd "$tmp"
		curl -fsSL -O "$url/runsc" -O "$url/runsc.sha512" -O "$url/containerd-shim-runsc-v1" -O "$url/containerd-shim-runsc-v1.sha512"
		sha512sum -c runsc.sha512 -c containerd-shim-runsc-v1.sha512
		chmod a+rx runsc containerd-shim-runsc-v1
		sudo mv runsc containerd-shim-runsc-v1 /usr/local/bin
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
