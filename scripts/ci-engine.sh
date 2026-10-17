#!/bin/sh
# Prepare a fresh CI runner to be one reference platform of the specs (specs/README.md). The runner is a throwaway VM, so it
# is changed freely: nothing here is meant for a developer machine.
#
#   scripts/ci-engine.sh docker | docker-gvisor | podman | podman-rootless
#
# Docker and Podman come with the runner image; this installs gVisor as Docker's default runtime and starts the
# Podman API socket the platform names. For podman-rootless the socket's directory is exported through $GITHUB_ENV, as the specs look for it there.
set -eu

# The specs' test registry on localhost:5000 speaks plain HTTP. Docker trusts localhost for that, Podman does not.
insecure_test_registry() {
	sudo mkdir -p /etc/containers/registries.conf.d
	printf '[[registry]]\nlocation = "localhost:5000"\ninsecure = true\n' | sudo tee /etc/containers/registries.conf.d/50-spec-registry.conf
}

platform=${1:?usage: ci-engine.sh docker|docker-gvisor|podman|podman-rootless}

case "$platform" in
docker)
	docker version
	;;
docker-gvisor)
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
	# registers the runtime `runsc` in /etc/docker/daemon.json and makes it the engine's default: egzo never chooses a runtime,
	# so gVisor is what the host is, and every container the engine starts runs under it
	# --reproduce-nat: Docker's embedded DNS (127.0.0.11, how a container finds `control` and `proxy` by name) is a NAT rule in the
	# container's network namespace, which gVisor's own network stack only reproduces when asked to
	sudo runsc install -- --reproduce-nat
	sudo jq '. + {"default-runtime": "runsc"}' /etc/docker/daemon.json | sudo tee /etc/docker/daemon.json.new
	sudo mv /etc/docker/daemon.json.new /etc/docker/daemon.json
	sudo systemctl restart docker
	runsc --version
	docker info --format '{{.DefaultRuntime}} {{json .Runtimes}}'
	docker run --rm alpine:3 dmesg | head -3
	;;
podman)
	insecure_test_registry
	# rootful Podman: the API service of root on /run/podman/podman.sock, which the runner user may use (the VM is thrown away).
	# systemd's podman.socket makes /run/podman readable by root only, so the service is started here instead.
	# Ubuntu's AppArmor puts a rootful Podman container's init and its exec'd processes in two stacked profiles as soon as
	# no-new-privileges is set, and they then cannot signal or inspect each other (known-issues/rootful-podman-apparmor-stacking.md).
	sudo mkdir -p /etc/containers/containers.conf.d
	printf '[containers]\napparmor_profile = "unconfined"\n' | sudo tee /etc/containers/containers.conf.d/50-spec-apparmor.conf
	sudo systemctl disable --now podman.socket podman.service 2>/dev/null || true
	sudo rm -rf /run/podman
	sudo mkdir -m 0755 /run/podman
	sudo nohup podman system service --time=0 unix:///run/podman/podman.sock >/tmp/podman-service.log 2>&1 &
	for _ in $(seq 1 30); do [ -S /run/podman/podman.sock ] && break; sleep 1; done
	[ -S /run/podman/podman.sock ] || { cat /tmp/podman-service.log; echo "the Podman socket did not appear" >&2; exit 1; }
	sudo chmod 0666 /run/podman/podman.sock
	ls -ld /run/podman /run/podman/podman.sock
	sudo podman version
	;;
podman-rootless)
	insecure_test_registry
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
