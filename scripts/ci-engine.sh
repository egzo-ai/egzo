#!/bin/sh
# Prepare a fresh CI runner to be one reference platform of the specs (specs/README.md). The runner is a throwaway VM, so it
# is changed freely: nothing here is meant for a developer machine.
#
#   scripts/ci-engine.sh docker | docker-gvisor | docker-sysbox | podman | podman-rootless
#
# Docker and Podman come with the runner image; this installs gVisor or sysbox as Docker's default runtime and starts
# the Podman API socket the platform names. For podman-rootless the socket's directory is exported through $GITHUB_ENV, as the specs look for it there.
set -eu

# The specs' test registry on localhost:5000 speaks plain HTTP. Docker trusts localhost for that, Podman does not.
insecure_test_registry() {
	sudo mkdir -p /etc/containers/registries.conf.d
	printf '[[registry]]\nlocation = "localhost:5000"\ninsecure = true\n' | sudo tee /etc/containers/registries.conf.d/50-spec-registry.conf
}

platform=${1:?usage: ci-engine.sh docker|docker-gvisor|docker-sysbox|podman|podman-rootless}

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
	# --host-uds=open: the link sidecar connects to the engine's socket, bind-mounted into it, which gVisor refuses by default (docs/gvisor.md)
	# --network=host: gVisor's own network stack cannot reach Docker's embedded DNS (127.0.0.11), so no container name resolves in it;
	# the sandbox then uses the container's kernel network stack, which weakens gVisor's network isolation (docs/gvisor.md)
	sudo runsc install -- --network=host --host-uds=open
	sudo jq '. + {"default-runtime": "runsc"}' /etc/docker/daemon.json | sudo tee /etc/docker/daemon.json.new
	sudo mv /etc/docker/daemon.json.new /etc/docker/daemon.json
	sudo systemctl restart docker
	runsc --version
	docker info --format '{{.DefaultRuntime}} {{json .Runtimes}}'
	# not piped straight into head: a container that does not start must fail this step
	started=$(docker run --rm alpine:3 dmesg)
	echo "$started" | head -3
	;;
docker-sysbox)
	# sysbox from its published package: an OCI runtime, a fork of runc, that puts every container in a Linux user
	# namespace (and virtualizes procfs and sysfs). Rootful Docker only, on a host with systemd, which the runner is.
	# The digests are the ones GitHub publishes for the release's assets.
	version=0.7.1
	case $(uname -m) in aarch64 | arm64) arch=arm64 ;; *) arch=amd64 ;; esac
	deb=sysbox-ce_${version}.linux_${arch}.deb
	case "$arch" in
	amd64) sum=9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5 ;;
	arm64) sum=04ca894ae0b53f0fa54eaacc173ce40363c9a95ea5450f773716a84ef650a69b ;;
	esac
	tmp=$(mktemp -d)
	(
		cd "$tmp"
		curl -fsSL -o "$deb" "https://github.com/nestybox/sysbox/releases/download/v${version}/${deb}"
		echo "$sum  $deb" | sha256sum -c
		# the package's postinst edits /etc/docker/daemon.json and its network settings with jq
		sudo apt-get install -y jq
		sudo apt-get install -y "./$deb"
	)
	rm -rf "$tmp"
	# makes it the engine's default: egzo never chooses a runtime, so sysbox is what the host is, and every container
	# the engine starts runs under it (docs/sysbox.md)
	sudo jq '. + {"default-runtime": "sysbox-runc"}' /etc/docker/daemon.json | sudo tee /etc/docker/daemon.json.new
	sudo mv /etc/docker/daemon.json.new /etc/docker/daemon.json
	sudo systemctl restart docker
	sysbox-runc --version
	docker info --format '{{.DefaultRuntime}} {{json .Runtimes}}'
	# Ubuntu 25.04 and later ship an AppArmor profile for fusermount3 that denies the FUSE mounts sysbox-fs makes under
	# /var/lib/sysboxfs, so no sysbox container starts (sysbox#947, open; the profile's local file is where its author says
	# to allow it). The runner is Ubuntu 26.04.
	if [ -f /etc/apparmor.d/fusermount3 ] && command -v apparmor_parser >/dev/null 2>&1; then
		sudo mkdir -p /etc/apparmor.d/local
		cat <<-'RULES' | sudo tee /etc/apparmor.d/local/fusermount3 >/dev/null
		# allow Sysbox to create fuse mounts (docs/sysbox.md; sysbox#947)
		mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
		mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
		umount /var/lib/sysboxfs/**/,
		RULES
		sudo apparmor_parser -r /etc/apparmor.d/fusermount3
		sudo systemctl restart sysbox
	fi
	# a container starts under it, and is in a user namespace: the ids inside are mapped, not the host's
	if ! started=$(docker run --rm alpine:3 cat /proc/self/uid_map); then
		systemctl status --no-pager sysbox sysbox-fs sysbox-mgr 2>&1 || true
		journalctl -u sysbox-fs -u sysbox-mgr -n 60 --no-pager 2>&1 || true
		exit 1
	fi
	echo "$started"
	[ "$(echo "$started" | awk 'NR==1 {print $2}')" != "0" ]
	# Diagnostic while sysbox support is a spike: a sidecar's settings (read-only root, non-root user, no capabilities,
	# a tmpfs it writes) and what the container sees of /run. Never fails the step.
	for tmpfs in /run /run/egzo; do
		echo "--- read-only root, user 65532, tmpfs at $tmpfs"
		docker run --rm --read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges \
			--tmpfs "$tmpfs:rw,nosuid,size=1m,mode=1777" alpine:3 sh -c \
			'id; ls -ld /run /run/egzo 2>&1; grep -E " /run| /tmp" /proc/self/mountinfo; mkdir -p /run/egzo && echo mkdir ok' || true
	done
	echo "--- read-only root, user 65532, a tmpfs at /run and one at /run/egzo (what the proxy and link get)"
	docker run --rm --read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges \
		--tmpfs "/run:rw,noexec,nosuid,size=64k,mode=1777" --tmpfs "/run/egzo:rw,nosuid,size=1m,mode=1777" alpine:3 sh -c \
		'ls -ld /run /run/egzo; grep -E " /run" /proc/self/mountinfo; touch /run/egzo/x && echo write ok' || true
	echo "--- the same, root filesystem writable"
	docker run --rm --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges \
		--tmpfs "/run:rw,nosuid,size=1m,mode=1777" alpine:3 sh -c \
		'ls -ld /run; grep -E " /run" /proc/self/mountinfo; mkdir -p /run/egzo && echo mkdir ok' || true
	;;
podman)
	insecure_test_registry
	# rootful Podman: the API service of root on /run/podman/podman.sock, which the runner user may use (the VM is thrown away).
	# systemd's podman.socket makes /run/podman readable by root only, so the service is started here instead.
	# Ubuntu's AppArmor puts a rootful Podman container's init and its exec'd processes in two stacked profiles as soon as
	# no-new-privileges is set, and they then cannot signal or inspect each other (docs/podman.md).
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
