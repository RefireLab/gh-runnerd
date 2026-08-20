package bake

import (
	"fmt"
	"strings"
)

// DefaultRunnerVersion is the pinned GitHub Actions runner release baked
// into the golden image.
const DefaultRunnerVersion = "2.336.0"

// installScript is executed inside the bake VM from the mounted seed share.
// It installs the guest agent, trust for the local registry, Docker, and the
// official GitHub Actions runner, then marks success with BAKE_OK.
func installScript(runnerVersion, runnerArch string, hasExtraRuns bool) string {
	var b strings.Builder
	b.WriteString(`#!/bin/bash
set -euo pipefail
SEED=/mnt/gh-runnerd-seed
install -m 0755 "$SEED/gh-runnerd-guest" /usr/local/bin/gh-runnerd-guest
install -m 0644 "$SEED/gh-runnerd-guest.service" /etc/systemd/system/gh-runnerd-guest.service
if [[ -s "$SEED/gh-runnerd-ca.crt" ]]; then
  install -m 0644 "$SEED/gh-runnerd-ca.crt" /usr/local/share/ca-certificates/gh-runnerd.crt
  update-ca-certificates || true
fi
cat "$SEED/hosts" >> /etc/hosts
install -d /etc/docker /etc/docker/certs.d/docker.io /etc/docker/certs.d/ghcr.io /etc/docker/certs.d/quay.io
install -m 0644 "$SEED/daemon.json" /etc/docker/daemon.json
install -m 0644 "$SEED/hosts.toml.docker" /etc/docker/certs.d/docker.io/hosts.toml
install -m 0644 "$SEED/hosts.toml.ghcr" /etc/docker/certs.d/ghcr.io/hosts.toml
install -m 0644 "$SEED/hosts.toml.quay" /etc/docker/certs.d/quay.io/hosts.toml

curl -fsSL https://get.docker.com | sh
apt-get install -y docker-compose-plugin || true
usermod -aG docker ubuntu || true

install -d -o ubuntu -g ubuntu /opt/actions-runner
cd /opt/actions-runner
`)
	fmt.Fprintf(&b, "curl -fL -o actions-runner.tgz \\\n  \"https://github.com/actions/runner/releases/download/v%s/actions-runner-linux-%s-%s.tar.gz\"\n",
		runnerVersion, runnerArch, runnerVersion)
	b.WriteString(`tar xzf actions-runner.tgz
rm -f actions-runner.tgz
chown -R ubuntu:ubuntu /opt/actions-runner
./bin/installdependencies.sh || apt-get install -y libicu74 libssl3t64 libkrb5-3 zlib1g || true

systemctl enable qemu-guest-agent.service || true
systemctl enable gh-runnerd-guest.service
systemctl disable cloud-init.service cloud-init-local.service cloud-config.service cloud-final.service || true
touch /etc/cloud/cloud-init.disabled

# cloud-init pinned netplan to the NIC it saw during bake (different PCI
# slot and MAC than at runtime), so runtime VMs never brought networking
# up. Replace it with a catch-all: any virtio NIC, DHCP from the host.
rm -f /etc/netplan/50-cloud-init.yaml
cat > /etc/netplan/50-gh-runnerd.yaml <<'NETPLAN'
network:
  version: 2
  ethernets:
    all-ethernet:
      match:
        name: "en*"
      dhcp4: true
      optional: true
NETPLAN
chmod 600 /etc/netplan/50-gh-runnerd.yaml
`)
	if hasExtraRuns {
		b.WriteString("bash \"$SEED/runnerfile-runs.sh\"\n")
	}
	b.WriteString("touch \"$SEED/BAKE_OK\"\n")
	return b.String()
}

// runsScript wraps Runnerfile RUN lines into a script executed at the end of
// the bake.
func runsScript(runs []string) string {
	return "#!/bin/bash\nset -euo pipefail\n" + strings.Join(runs, "\n") + "\n"
}

// userData is the cloud-init NoCloud configuration for the bake VM. It
// mounts the 9p seed share, runs install.sh, records failure in BAKE_FAIL,
// and powers the VM off when done.
func userData() string {
	return `#cloud-config
hostname: gh-runnerd-golden
manage_etc_hosts: false
package_update: true
packages:
  - qemu-guest-agent
  - git
  - curl
  - jq
  - tar
  - gzip
  - unzip
  - ca-certificates
  - apt-transport-https
  - gnupg
runcmd:
  - mkdir -p /mnt/gh-runnerd-seed
  - mount -t 9p -o trans=virtio,version=9p2000.L seed /mnt/gh-runnerd-seed
  - bash /mnt/gh-runnerd-seed/install.sh > /mnt/gh-runnerd-seed/install.log 2>&1 || touch /mnt/gh-runnerd-seed/BAKE_FAIL
power_state:
  mode: poweroff
  timeout: 1800
  condition: true
`
}

func metaData() string {
	return "instance-id: gh-runnerd-bake\nlocal-hostname: gh-runnerd-golden\n"
}
