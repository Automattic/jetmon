#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

LAB_DIR="${JETMON_ROLLOUT_LAB_DIR:-$HOME/rollout-lab}"
POOL="${JETMON_ROLLOUT_POOL:-jetmon-rollout}"
NETWORK="${JETMON_ROLLOUT_NETWORK:-default}"
PREFIX="${JETMON_ROLLOUT_PREFIX:-jetmon-rollout}"
IMAGE_URL="${JETMON_ROLLOUT_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
IMAGE_NAME="${JETMON_ROLLOUT_IMAGE_NAME:-noble-server-cloudimg-amd64.img}"
VM_USER="${JETMON_ROLLOUT_VM_USER:-jetmon}"
SSH_KEY="${JETMON_ROLLOUT_SSH_KEY:-$HOME/.ssh/jetmon-rollout-lab_ed25519}"
SSH_PUBKEY="${JETMON_ROLLOUT_SSH_PUBKEY:-$SSH_KEY.pub}"
LIBVIRT_URI="${JETMON_ROLLOUT_LIBVIRT_URI:-qemu:///system}"
DEFAULT_MEMORY_MIB="${JETMON_ROLLOUT_MEMORY_MIB:-2048}"
DEFAULT_VCPUS="${JETMON_ROLLOUT_VCPUS:-2}"
DEFAULT_DISK_GIB="${JETMON_ROLLOUT_DISK_GIB:-20}"
SSH_CONNECT_TIMEOUT="${JETMON_ROLLOUT_SSH_CONNECT_TIMEOUT:-5}"
WAIT_TIMEOUT="${JETMON_ROLLOUT_WAIT_TIMEOUT:-600}"
ACTIVITY_WAIT_TIMEOUT="${JETMON_ROLLOUT_ACTIVITY_WAIT_TIMEOUT:-240}"
JETMON2_BINARY="${JETMON_ROLLOUT_JETMON2_BINARY:-$REPO_ROOT/bin/jetmon2}"
JETMON2_SERVICE="${JETMON_ROLLOUT_JETMON2_SERVICE:-$REPO_ROOT/systemd/jetmon2.service}"
JETMON2_LOGROTATE="${JETMON_ROLLOUT_JETMON2_LOGROTATE:-$REPO_ROOT/systemd/jetmon2-logrotate}"
LAB_BUCKET_MIN="${JETMON_ROLLOUT_BUCKET_MIN:-0}"
LAB_BUCKET_MAX="${JETMON_ROLLOUT_BUCKET_MAX:-99}"
LAB_BUCKET_TOTAL="${JETMON_ROLLOUT_BUCKET_TOTAL:-1000}"

usage() {
	cat <<'USAGE'
usage: scripts/rollout-vm-lab.sh <command> [args]

Commands:
  doctor                         Verify host KVM/libvirt/image/key prerequisites.
  fetch-image                    Download the configured Ubuntu cloud image.
  create <role> [name]           Create one VM. Roles: db, v1, v2, generic.
  create-topology                Create db, v1, and v2 lab VMs.
  seed-db                        Seed v1-compatible site data into the DB VM.
  install-v1-sim                 Install/start the v1 simulator service.
  install-v2                     Stage jetmon2, config, and systemd unit on v2.
  migrate-v2                     Run jetmon2 migrations from the v2 VM.
  prepare-topology               Seed DB, install v1/v2, migrate, and smoke preflight.
  smoke-preflight                Run rollout host-preflight from the v2 VM.
  smoke-guided-dry-run           Print the guided fresh-server rollout plan on v2.
  smoke-guided-execute-rollback  Execute guided cutover, then guided rollback.
  smoke-failure-gates            Verify preflight refuses unsafe DB/systemd state.
  smoke-interrupted-resume       Interrupt after v1 stop, resume, then roll back.
  smoke-post-start-rollback      Fail a post-start gate and verify guided rollback.
  smoke-bad-ssh                  Verify bad v1 SSH commands fail before stopping v1.
  smoke-v2-start-failure         Verify v1 stays stopped when v2 service start fails.
  smoke-runtime-guards           Verify bad DB config and unwritable log dir refusals.
  smoke-real-activity            Start v2 and wait for real last_checked_at updates.
  snapshot-run <snapshot> <flow> Revert to snapshot, run flow, revert again.
  snapshot-run-all <snapshot>    Run all named snapshot-backed smoke flows.
  wait-ssh <vm>                  Wait until a VM has an IP and accepts SSH.
  ssh <vm> [command...]          SSH into a VM or run a command.
  snapshot <vm> <snapshot>       Create an offline libvirt snapshot.
  snapshot-all <snapshot>        Snapshot db, v1, and v2 VMs.
  revert <vm> <snapshot>         Revert a VM to a snapshot.
  revert-all <snapshot>          Revert db, v1, and v2 VMs.
  destroy <vm>                   Destroy and undefine one VM plus its lab volumes.
  destroy-topology               Destroy db, v1, and v2 lab VMs.
  list                           List lab VMs, network leases, and pool volumes.

Environment:
  JETMON_ROLLOUT_LAB_DIR         Default: ~/rollout-lab
  JETMON_ROLLOUT_POOL            Default: jetmon-rollout
  JETMON_ROLLOUT_NETWORK         Default: default
  JETMON_ROLLOUT_PREFIX          Default: jetmon-rollout
  JETMON_ROLLOUT_IMAGE_URL       Default: Ubuntu 24.04 noble cloud image
  JETMON_ROLLOUT_SSH_KEY         Default: ~/.ssh/jetmon-rollout-lab_ed25519
  JETMON_ROLLOUT_BUCKET_MIN      Default: 0
  JETMON_ROLLOUT_BUCKET_MAX      Default: 99
  JETMON_ROLLOUT_BUCKET_TOTAL    Default: 1000
  JETMON_ROLLOUT_ACTIVITY_WAIT_TIMEOUT
                                  Default: 240 seconds
USAGE
}

log() {
	printf 'INFO %s\n' "$*"
}

pass() {
	printf 'PASS %s\n' "$*"
}

warn() {
	printf 'WARN %s\n' "$*" >&2
}

fail() {
	printf 'FAIL %s\n' "$*" >&2
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

virsh_cmd() {
	virsh -c "$LIBVIRT_URI" "$@"
}

vm_name() {
	case "$1" in
	"$PREFIX"-*) printf '%s\n' "$1" ;;
	*) printf '%s-%s\n' "$PREFIX" "$1" ;;
	esac
}

role_from_vm() {
	local vm="$1"
	vm="${vm#"$PREFIX"-}"
	printf '%s\n' "$vm"
}

image_path() {
	if [[ -n "${JETMON_ROLLOUT_IMAGE_PATH:-}" ]]; then
		printf '%s\n' "$JETMON_ROLLOUT_IMAGE_PATH"
		return 0
	fi
	printf '%s/%s\n' "$(pool_path)" "$IMAGE_NAME"
}

disk_path() {
	printf '%s/%s.qcow2\n' "$(pool_path)" "$1"
}

seed_path() {
	printf '%s/%s-seed.iso\n' "$(pool_path)" "$1"
}

user_data_path() {
	printf '%s/cloud-init/%s-user-data.yaml\n' "$LAB_DIR" "$1"
}

meta_data_path() {
	printf '%s/cloud-init/%s-meta-data.yaml\n' "$LAB_DIR" "$1"
}

pool_path() {
	local target
	target="$(virsh_cmd pool-dumpxml "$POOL" | sed -n 's:.*<path>\(.*\)</path>.*:\1:p' | head -n 1)"
	[[ -n "$target" ]] || fail "could not determine path for pool $POOL"
	printf '%s\n' "$target"
}

ensure_lab_dirs() {
	mkdir -p "$LAB_DIR/images" "$LAB_DIR/cloud-init" "$LAB_DIR/logs" "$LAB_DIR/work"
}

doctor() {
	local image
	for cmd in virsh qemu-img virt-install cloud-localds ssh scp curl mysql sed awk; do
		need_cmd "$cmd"
	done
	[[ -e /dev/kvm ]] || fail "/dev/kvm does not exist"
	[[ -r /dev/kvm && -w /dev/kvm ]] || fail "/dev/kvm is not accessible to $(id -un)"
	pass "kvm_accessible=/dev/kvm"
	virsh_cmd list --all >/dev/null
	pass "libvirt_uri=$LIBVIRT_URI"
	virsh_cmd net-info "$NETWORK" >/dev/null
	pass "network=$NETWORK"
	virsh_cmd pool-info "$POOL" >/dev/null
	pass "pool=$POOL path=$(pool_path)"
	[[ -w "$(pool_path)" ]] || fail "pool path is not writable by $(id -un): $(pool_path)"
	pass "pool_writable=$(pool_path)"
	[[ -f "$SSH_KEY" ]] || fail "missing SSH key $SSH_KEY"
	[[ -f "$SSH_PUBKEY" ]] || fail "missing SSH public key $SSH_PUBKEY"
	pass "ssh_key=$SSH_KEY"
	image="$(image_path)"
	if [[ -f "$image" ]]; then
		pass "image=$image"
	else
		warn "image_missing=$image; run fetch-image"
	fi
}

fetch_image() {
	ensure_lab_dirs
	local image tmp
	image="$(image_path)"
	tmp="$image.tmp"
	mkdir -p "$(dirname "$image")"
	if [[ -s "$image" ]]; then
		pass "image_exists=$image"
		return 0
	fi
	log "download_image=$IMAGE_URL"
	curl -fL --retry 3 --retry-delay 2 -o "$tmp" "$IMAGE_URL"
	mv "$tmp" "$image"
	pass "image_downloaded=$image"
}

role_packages() {
	case "$1" in
	db)
		printf 'mariadb-server\nmariadb-client\n'
		;;
	v1 | v2 | generic)
		;;
	*)
		fail "unknown role $1"
		;;
	esac
}

write_cloud_init() {
	local role="$1"
	local vm="$2"
	local public_key
	public_key="$(<"$SSH_PUBKEY")"
	{
		printf '#cloud-config\n'
		printf 'hostname: %s\n' "$vm"
		printf 'manage_etc_hosts: true\n'
		printf 'users:\n'
		printf '  - default\n'
		printf '  - name: %s\n' "$VM_USER"
		printf '    gecos: Jetmon Rollout Lab\n'
		printf '    groups: sudo\n'
		printf '    shell: /bin/bash\n'
		printf '    sudo: ALL=(ALL) NOPASSWD:ALL\n'
		printf '    lock_passwd: true\n'
		printf '    ssh_authorized_keys:\n'
		printf '      - %s\n' "$public_key"
		printf 'package_update: true\n'
		printf 'packages:\n'
		printf '  - qemu-guest-agent\n'
		printf '  - curl\n'
		printf '  - ca-certificates\n'
		printf '  - git\n'
		printf '  - jq\n'
		printf '  - make\n'
		printf '  - netcat-openbsd\n'
		while IFS= read -r package; do
			[[ -n "$package" ]] && printf '  - %s\n' "$package"
		done < <(role_packages "$role")
		printf 'write_files:\n'
		printf '  - path: /etc/jetmon-rollout-lab-role\n'
		printf '    permissions: "0644"\n'
		printf '    content: |\n'
		printf '      %s\n' "$role"
		if [[ "$role" == "db" ]]; then
			printf '  - path: /etc/mysql/mariadb.conf.d/99-jetmon-rollout-lab.cnf\n'
			printf '    permissions: "0644"\n'
			printf '    content: |\n'
			printf '      [mysqld]\n'
			printf '      bind-address=0.0.0.0\n'
		fi
		printf 'runcmd:\n'
		printf '  - systemctl enable --now qemu-guest-agent\n'
		if [[ "$role" == "db" ]]; then
			printf '  - systemctl restart mariadb\n'
			printf '  - mysql -uroot -e "CREATE DATABASE IF NOT EXISTS jetmon_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"\n'
			printf '  - mysql -uroot -e "CREATE USER IF NOT EXISTS '\''jetmon'\''@'\''%%'\'' IDENTIFIED BY '\''jetmon'\'';"\n'
			printf '  - mysql -uroot -e "GRANT ALL PRIVILEGES ON jetmon_db.* TO '\''jetmon'\''@'\''%%'\'';"\n'
			printf '  - mysql -uroot -e "FLUSH PRIVILEGES;"\n'
		fi
		printf 'final_message: "jetmon rollout lab %s ready"\n' "$role"
	} >"$(user_data_path "$vm")"
	{
		printf 'instance-id: %s\n' "$vm"
		printf 'local-hostname: %s\n' "$vm"
	} >"$(meta_data_path "$vm")"
	cloud-localds "$(seed_path "$vm")" "$(user_data_path "$vm")" "$(meta_data_path "$vm")"
}

create_vm() {
	local role="$1"
	local requested="${2:-$role}"
	local vm disk seed image memory vcpus disk_gib
	vm="$(vm_name "$requested")"
	disk="$(disk_path "$vm")"
	seed="$(seed_path "$vm")"
	image="$(image_path)"
	memory="${JETMON_ROLLOUT_CREATE_MEMORY_MIB:-$DEFAULT_MEMORY_MIB}"
	vcpus="${JETMON_ROLLOUT_CREATE_VCPUS:-$DEFAULT_VCPUS}"
	disk_gib="${JETMON_ROLLOUT_CREATE_DISK_GIB:-$DEFAULT_DISK_GIB}"
	[[ "$role" == "db" ]] && memory="${JETMON_ROLLOUT_DB_MEMORY_MIB:-4096}"
	[[ "$role" == "db" ]] && disk_gib="${JETMON_ROLLOUT_DB_DISK_GIB:-30}"
	[[ -f "$image" ]] || fail "missing image $image; run fetch-image"
	if virsh_cmd dominfo "$vm" >/dev/null 2>&1; then
		fail "domain already exists: $vm"
	fi
	[[ ! -e "$disk" ]] || fail "disk already exists: $disk"
	write_cloud_init "$role" "$vm"
	qemu-img create -f qcow2 -F qcow2 -b "$image" "$disk" "${disk_gib}G" >/dev/null
	virt-install \
		--connect "$LIBVIRT_URI" \
		--name "$vm" \
		--memory "$memory" \
		--vcpus "$vcpus" \
		--cpu host \
		--os-variant ubuntu24.04 \
		--import \
		--disk "path=$disk,format=qcow2,bus=virtio" \
		--disk "path=$seed,device=cdrom" \
		--network "network=$NETWORK,model=virtio" \
		--channel unix,target.type=virtio,target.name=org.qemu.guest_agent.0 \
		--graphics none \
		--console pty,target_type=serial \
		--noautoconsole
	pass "vm_created=$vm role=$role disk=$disk seed=$seed"
}

create_topology() {
	create_vm db db
	create_vm v1 v1
	create_vm v2 v2
}

vm_ip() {
	local vm="$1"
	local ip
	ip="$(virsh_cmd net-dhcp-leases "$NETWORK" 2>/dev/null | awk -v vm="$vm" '$0 ~ vm && /ipv4/ {sub("/.*", "", $5); print $5; exit}')"
	if [[ -z "$ip" ]]; then
		ip="$(virsh_cmd domifaddr "$vm" --source lease 2>/dev/null | awk '/ipv4/ {sub("/.*", "", $4); print $4; exit}')"
	fi
	if [[ -z "$ip" ]]; then
		ip="$(virsh_cmd domifaddr "$vm" --source agent 2>/dev/null | awk '/ipv4/ {sub("/.*", "", $4); print $4; exit}')"
	fi
	printf '%s\n' "$ip"
}

wait_ssh() {
	local vm="$1"
	local deadline ip
	deadline=$((SECONDS + WAIT_TIMEOUT))
	while (( SECONDS < deadline )); do
		ip="$(vm_ip "$vm")"
		if [[ -n "$ip" ]]; then
			if ssh -i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout="$SSH_CONNECT_TIMEOUT" "$VM_USER@$ip" true >/dev/null 2>&1; then
				pass "ssh_ready vm=$vm ip=$ip"
				return 0
			fi
		fi
		sleep 5
	done
	fail "timed out waiting for SSH: $vm"
}

ssh_vm() {
	local vm="$1"
	shift || true
	local ip
	ip="$(vm_ip "$vm")"
	[[ -n "$ip" ]] || fail "no IP found for $vm"
	ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$VM_USER@$ip" "$@"
}

vm_ip_required() {
	local vm="$1"
	local ip
	ip="$(vm_ip "$vm")"
	[[ -n "$ip" ]] || fail "no IP found for $vm"
	printf '%s\n' "$ip"
}

scp_to_vm() {
	local src="$1"
	local vm="$2"
	local dest="$3"
	local ip
	ip="$(vm_ip_required "$vm")"
	scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$src" "$VM_USER@$ip:$dest"
}

mysql_lab() {
	local db_ip="$1"
	shift
	mysql --connect-timeout=5 -h "$db_ip" -u jetmon -pjetmon "$@"
}

wait_mysql() {
	local db_ip="$1"
	local deadline
	deadline=$((SECONDS + WAIT_TIMEOUT))
	while (( SECONDS < deadline )); do
		if mysql_lab "$db_ip" jetmon_db -e 'SELECT 1' >/dev/null 2>&1; then
			pass "mysql_ready host=$db_ip database=jetmon_db"
			return 0
		fi
		sleep 5
	done
	fail "timed out waiting for MySQL: $db_ip"
}

write_seed_sql() {
	ensure_lab_dirs
	cat >"$LAB_DIR/work/seed-db.sql" <<'SQL'
CREATE DATABASE IF NOT EXISTS jetmon_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'jetmon'@'%' IDENTIFIED BY 'jetmon';
GRANT ALL PRIVILEGES ON jetmon_db.* TO 'jetmon'@'%';
FLUSH PRIVILEGES;

USE jetmon_db;

CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
	jetpack_monitor_site_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
	blog_id BIGINT UNSIGNED NOT NULL,
	bucket_no SMALLINT UNSIGNED NOT NULL DEFAULT 0,
	monitor_url VARCHAR(300) NOT NULL DEFAULT '',
	monitor_active TINYINT UNSIGNED NOT NULL DEFAULT 0,
	site_status TINYINT NOT NULL DEFAULT 1,
	last_status_change DATETIME NULL,
	check_interval SMALLINT UNSIGNED NOT NULL DEFAULT 5,
	INDEX idx_bucket_active (bucket_no, monitor_active),
	INDEX blog_id_monitor_url (blog_id, monitor_url)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

DELETE FROM jetpack_monitor_sites WHERE blog_id BETWEEN 910001 AND 910010;

INSERT INTO jetpack_monitor_sites
	(blog_id, bucket_no, monitor_url, monitor_active, site_status, last_status_change, check_interval)
VALUES
	(910001, 0,  'https://example.com/',             1, 1, UTC_TIMESTAMP(), 5),
	(910002, 3,  'https://wordpress.com/',           1, 1, UTC_TIMESTAMP(), 5),
	(910003, 7,  'https://developer.wordpress.com/', 1, 1, UTC_TIMESTAMP(), 5),
	(910004, 15, 'https://jetpack.com/',             1, 1, UTC_TIMESTAMP(), 5),
	(910005, 24, 'https://automattic.com/',          1, 1, UTC_TIMESTAMP(), 5),
	(910006, 32, 'https://wp.com/',                  1, 1, UTC_TIMESTAMP(), 5),
	(910007, 49, 'https://woocommerce.com/',         1, 1, UTC_TIMESTAMP(), 5),
	(910008, 63, 'https://akismet.com/',             1, 1, UTC_TIMESTAMP(), 5),
	(910009, 81, 'https://gravatar.com/',            1, 1, UTC_TIMESTAMP(), 5),
	(910010, 99, 'https://wordpress.org/',           1, 1, UTC_TIMESTAMP(), 5);
SQL
}

seed_db() {
	local db_vm db_ip sql
	db_vm="$(vm_name db)"
	wait_ssh "$db_vm"
	db_ip="$(vm_ip_required "$db_vm")"
	wait_mysql "$db_ip"
	write_seed_sql
	sql="$LAB_DIR/work/seed-db.sql"
	scp_to_vm "$sql" "$db_vm" /tmp/jetmon-rollout-seed-db.sql
	ssh_vm "$db_vm" 'sudo mysql < /tmp/jetmon-rollout-seed-db.sql'
	pass "db_seeded vm=$db_vm host=$db_ip rows=10"
}

write_v1_sim_files() {
	ensure_lab_dirs
	cat >"$LAB_DIR/work/jetmon-v1-sim.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

log_dir=/opt/jetmon-v1-sim/logs
pid_file=/opt/jetmon-v1-sim/jetmon-v1-sim.pid
mkdir -p "$log_dir"
printf '%s\n' "$$" >"$pid_file"
trap 'rm -f "$pid_file"; exit 0' INT TERM

while true; do
	printf '%s bucket_range=%s-%s db=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${BUCKET_NO_MIN:-}" "${BUCKET_NO_MAX:-}" "${DB_HOST:-}" >>"$log_dir/jetmon-v1-sim.log"
	sleep 5
done
SH

	cat >"$LAB_DIR/work/jetmon-v1-sim.service" <<'SERVICE'
[Unit]
Description=Jetmon v1 rollout lab simulator
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=jetmon
Group=jetmon
EnvironmentFile=/etc/jetmon-v1-sim.env
ExecStart=/opt/jetmon-v1-sim/jetmon-v1-sim.sh
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
SERVICE
}

install_v1_sim() {
	local v1_vm db_vm db_ip env_file
	v1_vm="$(vm_name v1)"
	db_vm="$(vm_name db)"
	wait_ssh "$v1_vm"
	wait_ssh "$db_vm"
	db_ip="$(vm_ip_required "$db_vm")"
	write_v1_sim_files
	env_file="$LAB_DIR/work/jetmon-v1-sim.env"
	cat >"$env_file" <<ENV
BUCKET_NO_MIN=$LAB_BUCKET_MIN
BUCKET_NO_MAX=$LAB_BUCKET_MAX
DB_HOST=$db_ip
DB_NAME=jetmon_db
ENV
	ssh_vm "$v1_vm" 'mkdir -p /tmp/jetmon-v1-sim-upload'
	scp_to_vm "$LAB_DIR/work/jetmon-v1-sim.sh" "$v1_vm" /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.sh
	scp_to_vm "$LAB_DIR/work/jetmon-v1-sim.service" "$v1_vm" /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.service
	scp_to_vm "$env_file" "$v1_vm" /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.env
	ssh_vm "$v1_vm" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo install -d -o jetmon -g jetmon -m 0755 /opt/jetmon-v1-sim /opt/jetmon-v1-sim/logs
sudo install -o jetmon -g jetmon -m 0755 /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.sh /opt/jetmon-v1-sim/jetmon-v1-sim.sh
sudo install -o root -g root -m 0644 /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.service /etc/systemd/system/jetmon-v1-sim.service
sudo install -o root -g root -m 0644 /tmp/jetmon-v1-sim-upload/jetmon-v1-sim.env /etc/jetmon-v1-sim.env
sudo systemctl daemon-reload
sudo systemctl enable --now jetmon-v1-sim
systemctl is-active --quiet jetmon-v1-sim
REMOTE
	pass "v1_sim_installed vm=$v1_vm bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
}

write_v2_lab_files() {
	local db_ip="$1"
	local v1_ip="$2"
	ensure_lab_dirs
	cat >"$LAB_DIR/work/config.json" <<JSON
{
	"AUTH_TOKEN": "jetmon-rollout-lab",
	"NUM_WORKERS": 10,
	"NUM_TO_PROCESS": 10,
	"DATASET_SIZE": 25,
	"WORKER_MAX_MEM_MB": 256,
	"LEGACY_STATUS_PROJECTION_ENABLE": true,
	"BUCKET_TOTAL": $LAB_BUCKET_TOTAL,
	"BUCKET_TARGET": 500,
	"BUCKET_HEARTBEAT_GRACE_SEC": 600,
	"PINNED_BUCKET_MIN": $LAB_BUCKET_MIN,
	"PINNED_BUCKET_MAX": $LAB_BUCKET_MAX,
	"BATCH_SIZE": 8,
	"VERIFLIER_BATCH_SIZE": 10,
	"SQL_UPDATE_BATCH": 1,
	"DB_CONFIG_UPDATES_MIN": 10,
	"PEER_OFFLINE_LIMIT": 1,
	"NUM_OF_CHECKS": 3,
	"TIME_BETWEEN_CHECKS_SEC": 30,
	"ALERT_COOLDOWN_MINUTES": 30,
	"STATS_UPDATE_INTERVAL_MS": 10000,
	"TIME_BETWEEN_NOTICES_MIN": 59,
	"MIN_TIME_BETWEEN_ROUNDS_SEC": 300,
	"NET_COMMS_TIMEOUT": 10,
	"LOG_FORMAT": "text",
	"DASHBOARD_PORT": 0,
	"API_PORT": 0,
	"DEBUG_PORT": 0,
	"EMAIL_TRANSPORT": "stub",
	"EMAIL_FROM": "jetmon@noreply.invalid",
	"VERIFIERS": []
}
JSON
	cat >"$LAB_DIR/work/jetmon2.env" <<ENV
DB_HOST=$db_ip
DB_PORT=3306
DB_USER=jetmon
DB_PASSWORD=jetmon
DB_NAME=jetmon_db
ENV
	{
		printf 'host,bucket_min,bucket_max\n'
		if (( LAB_BUCKET_MIN > 0 )); then
			printf '%s-before,0,%d\n' "$PREFIX" "$((LAB_BUCKET_MIN - 1))"
		fi
		printf '%s,%d,%d\n' "$(vm_name v1)" "$LAB_BUCKET_MIN" "$LAB_BUCKET_MAX"
		if (( LAB_BUCKET_MAX + 1 < LAB_BUCKET_TOTAL )); then
			printf '%s-after,%d,%d\n' "$PREFIX" "$((LAB_BUCKET_MAX + 1))" "$((LAB_BUCKET_TOTAL - 1))"
		fi
	} >"$LAB_DIR/work/rollout-buckets.csv"
	cat >"$LAB_DIR/work/v2-ssh-config" <<SSHCONFIG
Host $(vm_name v1)
  HostName $v1_ip
  User $VM_USER
  IdentityFile ~/.ssh/jetmon-rollout-lab_ed25519
  BatchMode yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
SSHCONFIG
}

install_v2() {
	local v2_vm v1_vm db_vm db_ip v1_ip
	v2_vm="$(vm_name v2)"
	v1_vm="$(vm_name v1)"
	db_vm="$(vm_name db)"
	[[ -x "$JETMON2_BINARY" ]] || fail "missing executable jetmon2 binary: $JETMON2_BINARY"
	[[ -f "$JETMON2_SERVICE" ]] || fail "missing systemd unit: $JETMON2_SERVICE"
	[[ -f "$JETMON2_LOGROTATE" ]] || fail "missing logrotate file: $JETMON2_LOGROTATE"
	wait_ssh "$v2_vm"
	wait_ssh "$v1_vm"
	wait_ssh "$db_vm"
	db_ip="$(vm_ip_required "$db_vm")"
	v1_ip="$(vm_ip_required "$v1_vm")"
	write_v2_lab_files "$db_ip" "$v1_ip"
	ssh_vm "$v2_vm" 'mkdir -p /tmp/jetmon-v2-upload'
	scp_to_vm "$JETMON2_BINARY" "$v2_vm" /tmp/jetmon-v2-upload/jetmon2
	scp_to_vm "$JETMON2_SERVICE" "$v2_vm" /tmp/jetmon-v2-upload/jetmon2.service
	scp_to_vm "$JETMON2_LOGROTATE" "$v2_vm" /tmp/jetmon-v2-upload/jetmon2-logrotate
	scp_to_vm "$LAB_DIR/work/config.json" "$v2_vm" /tmp/jetmon-v2-upload/config.json
	scp_to_vm "$LAB_DIR/work/jetmon2.env" "$v2_vm" /tmp/jetmon-v2-upload/jetmon2.env
	scp_to_vm "$LAB_DIR/work/rollout-buckets.csv" "$v2_vm" /tmp/jetmon-v2-upload/rollout-buckets.csv
	scp_to_vm "$LAB_DIR/work/v2-ssh-config" "$v2_vm" /tmp/jetmon-v2-upload/ssh-config
	scp_to_vm "$SSH_KEY" "$v2_vm" /tmp/jetmon-v2-upload/jetmon-rollout-lab_ed25519
	scp_to_vm "$SSH_PUBKEY" "$v2_vm" /tmp/jetmon-v2-upload/jetmon-rollout-lab_ed25519.pub
	ssh_vm "$v2_vm" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo install -d -o jetmon -g jetmon -m 0755 /opt/jetmon2 /opt/jetmon2/config /opt/jetmon2/logs /opt/jetmon2/logs/rollout /opt/jetmon2/stats
sudo install -o root -g root -m 0755 /tmp/jetmon-v2-upload/jetmon2 /opt/jetmon2/jetmon2
sudo install -o jetmon -g jetmon -m 0644 /tmp/jetmon-v2-upload/config.json /opt/jetmon2/config/config.json
sudo install -o root -g jetmon -m 0640 /tmp/jetmon-v2-upload/jetmon2.env /opt/jetmon2/config/jetmon2.env
sudo install -o jetmon -g jetmon -m 0644 /tmp/jetmon-v2-upload/rollout-buckets.csv /opt/jetmon2/rollout-buckets.csv
sudo install -o root -g root -m 0644 /tmp/jetmon-v2-upload/jetmon2.service /etc/systemd/system/jetmon2.service
sudo install -o root -g root -m 0644 /tmp/jetmon-v2-upload/jetmon2-logrotate /etc/logrotate.d/jetmon2
sudo install -d -o jetmon -g jetmon -m 0700 /home/jetmon/.ssh
sudo install -o jetmon -g jetmon -m 0600 /tmp/jetmon-v2-upload/jetmon-rollout-lab_ed25519 /home/jetmon/.ssh/jetmon-rollout-lab_ed25519
sudo install -o jetmon -g jetmon -m 0644 /tmp/jetmon-v2-upload/jetmon-rollout-lab_ed25519.pub /home/jetmon/.ssh/jetmon-rollout-lab_ed25519.pub
sudo install -o jetmon -g jetmon -m 0600 /tmp/jetmon-v2-upload/ssh-config /home/jetmon/.ssh/config
sudo chown -R jetmon:jetmon /opt/jetmon2/logs /opt/jetmon2/stats
sudo systemctl daemon-reload
sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true
sudo systemd-analyze verify /etc/systemd/system/jetmon2.service
cd /opt/jetmon2
set -a
. /opt/jetmon2/config/jetmon2.env
set +a
sudo -u jetmon env \
	JETMON_CONFIG=/opt/jetmon2/config/config.json \
	DB_HOST="$DB_HOST" DB_PORT="$DB_PORT" DB_USER="$DB_USER" DB_PASSWORD="$DB_PASSWORD" DB_NAME="$DB_NAME" \
	/opt/jetmon2/jetmon2 validate-config
REMOTE
	pass "v2_installed vm=$v2_vm db_host=$db_ip plan=/opt/jetmon2/rollout-buckets.csv"
}

migrate_v2() {
	local v2_vm db_vm
	v2_vm="$(vm_name v2)"
	db_vm="$(vm_name db)"
	wait_ssh "$v2_vm"
	wait_ssh "$db_vm"
	ssh_vm "$v2_vm" 'bash -s' <<'REMOTE'
set -euo pipefail
cd /opt/jetmon2
set -a
. /opt/jetmon2/config/jetmon2.env
set +a
sudo -u jetmon env \
	JETMON_CONFIG=/opt/jetmon2/config/config.json \
	DB_HOST="$DB_HOST" DB_PORT="$DB_PORT" DB_USER="$DB_USER" DB_PASSWORD="$DB_PASSWORD" DB_NAME="$DB_NAME" \
	/opt/jetmon2/jetmon2 migrate
REMOTE
	mark_lab_activity
	pass "v2_migrations_applied vm=$v2_vm"
}

mark_lab_activity() {
	local db_vm db_ip
	db_vm="$(vm_name db)"
	db_ip="$(vm_ip_required "$db_vm")"
	mysql_lab "$db_ip" jetmon_db -e "UPDATE jetpack_monitor_sites SET last_checked_at = UTC_TIMESTAMP() WHERE bucket_no BETWEEN $LAB_BUCKET_MIN AND $LAB_BUCKET_MAX"
	pass "lab_activity_marked bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
}

clear_lab_activity() {
	local db_vm db_ip
	db_vm="$(vm_name db)"
	db_ip="$(vm_ip_required "$db_vm")"
	mysql_lab "$db_ip" jetmon_db -e "UPDATE jetpack_monitor_sites SET last_checked_at = NULL WHERE bucket_no BETWEEN $LAB_BUCKET_MIN AND $LAB_BUCKET_MAX"
	pass "lab_activity_cleared bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
}

lab_active_site_count() {
	local db_ip="$1"
	mysql_lab "$db_ip" --batch --skip-column-names jetmon_db -e "SELECT COUNT(*) FROM jetpack_monitor_sites WHERE monitor_active = 1 AND bucket_no BETWEEN $LAB_BUCKET_MIN AND $LAB_BUCKET_MAX" | tr -d '[:space:]'
}

lab_checked_site_count() {
	local db_ip="$1"
	mysql_lab "$db_ip" --batch --skip-column-names jetmon_db -e "SELECT COUNT(*) FROM jetpack_monitor_sites WHERE monitor_active = 1 AND bucket_no BETWEEN $LAB_BUCKET_MIN AND $LAB_BUCKET_MAX AND last_checked_at IS NOT NULL" | tr -d '[:space:]'
}

wait_for_real_lab_activity() {
	local db_vm db_ip active checked deadline
	db_vm="$(vm_name db)"
	db_ip="$(vm_ip_required "$db_vm")"
	active="$(lab_active_site_count "$db_ip")"
	[[ "$active" =~ ^[0-9]+$ ]] || {
		warn "invalid active site count: $active"
		return 1
	}
	if (( active == 0 )); then
		warn "no active lab sites in bucket range $LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
		return 1
	fi

	deadline=$((SECONDS + ACTIVITY_WAIT_TIMEOUT))
	while (( SECONDS < deadline )); do
		checked="$(lab_checked_site_count "$db_ip")"
		[[ "$checked" =~ ^[0-9]+$ ]] || {
			warn "invalid checked site count: $checked"
			return 1
		}
		if (( checked >= active )); then
			pass "real_activity_seen checked=$checked active=$active bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
			return 0
		fi
		log "waiting_for_real_activity checked=$checked active=$active timeout_seconds=$ACTIVITY_WAIT_TIMEOUT"
		sleep 5
	done
	warn "timed out waiting for real activity checked=$(lab_checked_site_count "$db_ip") active=$active bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
	return 1
}

future_activity_cutoff() {
	date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ
}

smoke_preflight() {
	local v2_vm
	v2_vm="$(vm_name v2)"
	wait_ssh "$v2_vm"
	ssh_vm "$v2_vm" "bash -lc 'cd /opt/jetmon2 && set -a && . config/jetmon2.env && set +a && JETMON_CONFIG=config/config.json ./jetmon2 rollout host-preflight --file rollout-buckets.csv --host $(vm_name v1) --runtime-host $(vm_name v2) --bucket-min $LAB_BUCKET_MIN --bucket-max $LAB_BUCKET_MAX --bucket-total $LAB_BUCKET_TOTAL'"
	pass "smoke_preflight_passed vm=$v2_vm"
}

smoke_guided_dry_run() {
	local v2_vm
	v2_vm="$(vm_name v2)"
	wait_ssh "$v2_vm"
	ssh_vm "$v2_vm" 'bash -s' <<REMOTE
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
JETMON_CONFIG=config/config.json ./jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $(vm_name v1) \\
	--runtime-host $(vm_name v2) \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'ssh $(vm_name v1) sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'ssh $(vm_name v1) sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--skip-status \\
	--dry-run
REMOTE
	pass "smoke_guided_dry_run_passed vm=$v2_vm"
}

reset_guided_lab_state() {
	local v1_vm v2_vm
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	wait_ssh "$v1_vm"
	wait_ssh "$v2_vm"
	ssh_vm "$v2_vm" 'sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true; sudo rm -f /opt/jetmon2/logs/rollout/*.state.json'
	ssh_vm "$v1_vm" 'sudo systemctl start jetmon-v1-sim; systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" '! systemctl is-enabled --quiet jetmon2'
	pass "guided_lab_state_reset v1=$v1_vm v2=$v2_vm"
}

smoke_guided_execute_rollback() {
	local v1_vm v2_vm
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	reset_guided_lab_state
	mark_lab_activity
	ssh_vm "$v2_vm" 'bash -s' <<REMOTE
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' \\
	'y' \\
	'y' \\
	'y' \\
	'STOP $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'START V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'READY' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
	ssh_vm "$v1_vm" '! systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'systemctl is-active --quiet jetmon2'
	mark_lab_activity
	ssh_vm "$v2_vm" 'bash -s' <<REMOTE
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n' \\
	'RESUME' \\
	'STOP V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'START V1 $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status \\
	--rollback
REMOTE
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	ssh_vm "$v1_vm" 'systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'sudo systemctl disable jetmon2 >/dev/null 2>&1 || true'
	pass "smoke_guided_execute_rollback_passed v1=$v1_vm v2=$v2_vm"
}

smoke_interrupted_resume() {
	local v1_vm v2_vm out
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	ensure_lab_dirs
	reset_guided_lab_state
	mark_lab_activity

	out="$LAB_DIR/logs/interrupted-resume-first.out"
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n' \\
	'y' \\
	'y' \\
	'y' \\
	'STOP $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
		fail "interrupted guided run unexpectedly completed"
	fi
	grep -q 'PASS guided_step=stop-v1' "$out" || {
		cat "$out"
		fail "interrupted guided run did not complete stop-v1"
	}
	ssh_vm "$v1_vm" '! systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	pass "interrupted_after_v1_stop output=$out"

	out="$LAB_DIR/logs/interrupted-resume-complete.out"
	ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n' \\
	'RESUME' \\
	'START V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'READY' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
	grep -q 'previous_state=resumed' "$out" || {
		cat "$out"
		fail "resume run did not resume previous state"
	}
	grep -q 'SKIP step=stop-v1 reason=completed_from_state' "$out" || {
		cat "$out"
		fail "resume run did not skip completed stop-v1"
	}
	grep -q 'PASS guided_rollout=complete' "$out" || {
		cat "$out"
		fail "resume run did not complete guided rollout"
	}
	ssh_vm "$v1_vm" '! systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'systemctl is-active --quiet jetmon2'
	pass "interrupted_resume_completed output=$out"

	out="$LAB_DIR/logs/interrupted-resume-rollback.out"
	ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n' \\
	'RESUME' \\
	'STOP V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'START V1 $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status \\
	--rollback
REMOTE
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	ssh_vm "$v1_vm" 'systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'sudo systemctl disable jetmon2 >/dev/null 2>&1 || true'
	pass "smoke_interrupted_resume_passed v1=$v1_vm v2=$v2_vm"
}

smoke_post_start_rollback() {
	local v1_vm v2_vm out future_since
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	ensure_lab_dirs
	reset_guided_lab_state
	future_since="$(future_activity_cutoff)"
	out="$LAB_DIR/logs/post-start-rollback.out"
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n' \\
	'y' \\
	'y' \\
	'y' \\
	'STOP $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'START V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'b' \\
	'STOP V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'y' \\
	'START V1 $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--since '$future_since' \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
		fail "post-start rollback flow unexpectedly exited successfully"
	fi
	grep -q 'PASS guided_step=start-v2' "$out" || {
		cat "$out"
		fail "post-start rollback flow did not reach start-v2"
	}
	grep -q 'PASS guided_rollback=complete' "$out" || {
		cat "$out"
		fail "post-start rollback flow did not complete rollback"
	}
	grep -q 'guided_rollout=rolled_back' "$out" || {
		cat "$out"
		fail "post-start rollback flow did not report rolled_back"
	}
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	ssh_vm "$v1_vm" 'systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'sudo systemctl disable jetmon2 >/dev/null 2>&1 || true'
	pass "smoke_post_start_rollback_passed v1=$v1_vm v2=$v2_vm output=$out"
}

smoke_bad_ssh() {
	local v1_vm v2_vm out
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	ensure_lab_dirs
	reset_guided_lab_state
	mark_lab_activity
	out="$LAB_DIR/logs/bad-ssh.out"
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n%s\n' \\
	'y' \\
	'y' \\
	'y' \\
	'STOP $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	's' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm-missing sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
		fail "bad SSH guided run unexpectedly passed"
	fi
	grep -q 'FAIL step=stop-v1' "$out" || {
		cat "$out"
		fail "bad SSH flow failed before expected stop-v1 failure"
	}
	ssh_vm "$v1_vm" 'systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	pass "smoke_bad_ssh_passed v1=$v1_vm v2=$v2_vm output=$out"
}

smoke_v2_start_failure() {
	local v1_vm v2_vm out run_status
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	ensure_lab_dirs
	reset_guided_lab_state
	mark_lab_activity

	ssh_vm "$v2_vm" 'sudo cp /etc/systemd/system/jetmon2.service /tmp/jetmon2.service.rollout-lab-good; sudo sed -i "/^ExecStart=/i ExecStartPre=/bin/false" /etc/systemd/system/jetmon2.service; sudo systemctl daemon-reload; sudo systemctl reset-failed jetmon2 >/dev/null 2>&1 || true'
	out="$LAB_DIR/logs/v2-start-failure.out"
	run_status=0
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
printf '%s\n%s\n%s\n%s\n%s\n%s\n' \\
	'y' \\
	'y' \\
	'y' \\
	'STOP $v1_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	'START V2 $v2_vm $LAB_BUCKET_MIN-$LAB_BUCKET_MAX' \\
	's' | sudo env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'sudo -u jetmon ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'sudo -u jetmon ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir logs/rollout \\
	--execute-operator-commands \\
	--skip-status
REMOTE
		run_status=0
	else
		run_status=$?
	fi
	ssh_vm "$v2_vm" 'sudo mv /tmp/jetmon2.service.rollout-lab-good /etc/systemd/system/jetmon2.service; sudo systemctl daemon-reload; sudo systemctl reset-failed jetmon2 >/dev/null 2>&1 || true; sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true'
	if (( run_status == 0 )); then
		cat "$out"
		fail "v2 start failure guided run unexpectedly passed"
	fi
	grep -q 'PASS guided_step=stop-v1' "$out" || {
		cat "$out"
		fail "v2 start failure flow did not stop v1 first"
	}
	grep -q 'FAIL step=start-v2' "$out" || {
		cat "$out"
		fail "v2 start failure flow did not fail at start-v2"
	}
	ssh_vm "$v1_vm" '! systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" '! systemctl is-active --quiet jetmon2'
	ssh_vm "$v1_vm" 'sudo systemctl start jetmon-v1-sim; systemctl is-active --quiet jetmon-v1-sim'
	ssh_vm "$v2_vm" 'sudo rm -f /opt/jetmon2/logs/rollout/*.state.json'
	pass "smoke_v2_start_failure_passed v1=$v1_vm v2=$v2_vm output=$out"
}

smoke_runtime_guards() {
	local v1_vm v2_vm out
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	ensure_lab_dirs
	reset_guided_lab_state

	out="$LAB_DIR/logs/unwritable-log-dir.out"
	ssh_vm "$v2_vm" 'sudo rm -rf /tmp/jetmon-unwritable-rollout; sudo install -d -o root -g root -m 0500 /tmp/jetmon-unwritable-rollout'
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
sudo -u jetmon env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT="\$DB_PORT" DB_USER="\$DB_USER" DB_PASSWORD="\$DB_PASSWORD" DB_NAME="\$DB_NAME" \\
	/opt/jetmon2/jetmon2 rollout guided \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL \\
	--mode fresh-server \\
	--v1-stop-command 'ssh $v1_vm sudo systemctl stop jetmon-v1-sim' \\
	--v1-start-command 'ssh $v1_vm sudo systemctl start jetmon-v1-sim' \\
	--log-dir /tmp/jetmon-unwritable-rollout \\
	--skip-status \\
	--dry-run
REMOTE
		ssh_vm "$v2_vm" 'sudo rm -rf /tmp/jetmon-unwritable-rollout'
		fail "unwritable log dir guided run unexpectedly passed"
	fi
	ssh_vm "$v2_vm" 'sudo rm -rf /tmp/jetmon-unwritable-rollout'
	grep -q 'rollout log directory preflight failed' "$out" || {
		cat "$out"
		fail "unwritable log dir failed for an unexpected reason"
	}
	pass "runtime_guard_unwritable_log_dir_refused output=$out"

	out="$LAB_DIR/logs/bad-db-preflight.out"
	if ssh_vm "$v2_vm" 'bash -s' <<REMOTE >"$out" 2>&1; then
set -euo pipefail
cd /opt/jetmon2
set -a
. config/jetmon2.env
set +a
sudo -u jetmon env \\
	JETMON_CONFIG=/opt/jetmon2/config/config.json \\
	DB_HOST="\$DB_HOST" DB_PORT=1 DB_USER="\$DB_USER" DB_PASSWORD=wrong DB_NAME="\$DB_NAME" \\
	timeout 20s /opt/jetmon2/jetmon2 rollout host-preflight \\
	--file rollout-buckets.csv \\
	--host $v1_vm \\
	--runtime-host $v2_vm \\
	--bucket-min $LAB_BUCKET_MIN \\
	--bucket-max $LAB_BUCKET_MAX \\
	--bucket-total $LAB_BUCKET_TOTAL
REMOTE
		fail "bad DB host-preflight unexpectedly passed"
	fi
	grep -q 'db connect' "$out" || {
		cat "$out"
		fail "bad DB host-preflight failed for an unexpected reason"
	}
	pass "runtime_guard_bad_db_refused output=$out"
}

smoke_real_activity() {
	local v1_vm v2_vm
	v1_vm="$(vm_name v1)"
	v2_vm="$(vm_name v2)"
	reset_guided_lab_state
	clear_lab_activity
	ssh_vm "$v1_vm" 'sudo systemctl stop jetmon-v1-sim; ! systemctl is-active --quiet jetmon-v1-sim'
	if ! ssh_vm "$v2_vm" 'sudo systemctl enable --now jetmon2; systemctl is-active --quiet jetmon2'; then
		ssh_vm "$v2_vm" 'sudo journalctl -u jetmon2 -n 80 --no-pager || true'
		ssh_vm "$v2_vm" 'sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true'
		ssh_vm "$v1_vm" 'sudo systemctl start jetmon-v1-sim'
		fail "v2 service did not start for real activity smoke"
	fi
	if ! wait_for_real_lab_activity; then
		ssh_vm "$v2_vm" 'sudo journalctl -u jetmon2 -n 120 --no-pager || true'
		ssh_vm "$v2_vm" 'sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true'
		ssh_vm "$v1_vm" 'sudo systemctl start jetmon-v1-sim'
		fail "real activity smoke did not observe last_checked_at updates"
	fi
	ssh_vm "$v2_vm" 'sudo systemctl disable --now jetmon2 >/dev/null 2>&1 || true; ! systemctl is-enabled --quiet jetmon2'
	ssh_vm "$v1_vm" 'sudo systemctl start jetmon-v1-sim; systemctl is-active --quiet jetmon-v1-sim'
	pass "smoke_real_activity_passed v1=$v1_vm v2=$v2_vm"
}

smoke_failure_gates() {
	local v2_vm db_vm db_ip out
	v2_vm="$(vm_name v2)"
	db_vm="$(vm_name db)"
	wait_ssh "$v2_vm"
	wait_ssh "$db_vm"
	ensure_lab_dirs
	db_ip="$(vm_ip_required "$db_vm")"

	out="$LAB_DIR/logs/overlap-preflight.out"
	mysql_lab "$db_ip" jetmon_db -e "INSERT INTO jetmon_hosts (host_id, bucket_min, bucket_max, status) VALUES ('jetmon-rollout-overlap-test', $LAB_BUCKET_MIN, $LAB_BUCKET_MAX, 'active') ON DUPLICATE KEY UPDATE bucket_min = VALUES(bucket_min), bucket_max = VALUES(bucket_max), status = VALUES(status), last_heartbeat = UTC_TIMESTAMP()"
	if ssh_vm "$v2_vm" "bash -lc 'cd /opt/jetmon2 && set -a && . config/jetmon2.env && set +a && JETMON_CONFIG=config/config.json ./jetmon2 rollout host-preflight --file rollout-buckets.csv --host $(vm_name v1) --runtime-host $(vm_name v2) --bucket-min $LAB_BUCKET_MIN --bucket-max $LAB_BUCKET_MAX --bucket-total $LAB_BUCKET_TOTAL'" >"$out" 2>&1; then
		mysql_lab "$db_ip" jetmon_db -e "DELETE FROM jetmon_hosts WHERE host_id = 'jetmon-rollout-overlap-test'"
		fail "overlap preflight unexpectedly passed"
	fi
	mysql_lab "$db_ip" jetmon_db -e "DELETE FROM jetmon_hosts WHERE host_id = 'jetmon-rollout-overlap-test'"
	grep -q 'overlapping pinned range' "$out" || {
		cat "$out"
		fail "overlap preflight failed for an unexpected reason"
	}
	pass "failure_gate_overlap_refused output=$out"

	out="$LAB_DIR/logs/bad-systemd-preflight.out"
	ssh_vm "$v2_vm" "printf '%s\n' '[Unit]' 'Description=Broken Jetmon lab unit' '[Service]' 'ExecStart=/does/not/exist' | sudo tee /tmp/jetmon-bad.service >/dev/null"
	if ssh_vm "$v2_vm" "bash -lc 'cd /opt/jetmon2 && set -a && . config/jetmon2.env && set +a && JETMON_CONFIG=config/config.json ./jetmon2 rollout host-preflight --file rollout-buckets.csv --host $(vm_name v1) --runtime-host $(vm_name v2) --bucket-min $LAB_BUCKET_MIN --bucket-max $LAB_BUCKET_MAX --bucket-total $LAB_BUCKET_TOTAL --systemd-unit /tmp/jetmon-bad.service'" >"$out" 2>&1; then
		fail "bad systemd preflight unexpectedly passed"
	fi
	grep -qi 'systemd' "$out" || {
		cat "$out"
		fail "bad systemd preflight failed for an unexpected reason"
	}
	pass "failure_gate_bad_systemd_refused output=$out"
}

wait_topology_ssh() {
	wait_ssh "$(vm_name db)"
	wait_ssh "$(vm_name v1)"
	wait_ssh "$(vm_name v2)"
}

snapshot_exists() {
	local vm="$1"
	local snapshot="$2"
	virsh_cmd snapshot-info "$vm" "$snapshot" >/dev/null 2>&1
}

validate_snapshot_all_exists() {
	local snapshot="$1"
	local role vm
	for role in db v1 v2; do
		vm="$(vm_name "$role")"
		snapshot_exists "$vm" "$snapshot" || fail "missing snapshot $snapshot for $vm; create it with snapshot-all $snapshot"
	done
}

run_flow_by_name() {
	case "$1" in
	execute-rollback) smoke_guided_execute_rollback ;;
	interrupted-resume) smoke_interrupted_resume ;;
	post-start-rollback) smoke_post_start_rollback ;;
	bad-ssh) smoke_bad_ssh ;;
	v2-start-failure) smoke_v2_start_failure ;;
	runtime-guards) smoke_runtime_guards ;;
	real-activity) smoke_real_activity ;;
	failure-gates) smoke_failure_gates ;;
	*) fail "unknown snapshot flow $1 (want: execute-rollback, interrupted-resume, post-start-rollback, bad-ssh, v2-start-failure, runtime-guards, real-activity, failure-gates)" ;;
	esac
}

SNAPSHOT_RUN_CLEANUP_ACTIVE=0
SNAPSHOT_RUN_CLEANUP_NAME=""

cleanup_snapshot_run() {
	if [[ "$SNAPSHOT_RUN_CLEANUP_ACTIVE" == "1" && -n "$SNAPSHOT_RUN_CLEANUP_NAME" ]]; then
		warn "snapshot_flow_cleanup snapshot=$SNAPSHOT_RUN_CLEANUP_NAME"
		revert_all "$SNAPSHOT_RUN_CLEANUP_NAME" || true
	fi
}

snapshot_run() {
	local snapshot="$1"
	local flow="$2"
	validate_snapshot_all_exists "$snapshot"
	SNAPSHOT_RUN_CLEANUP_ACTIVE=1
	SNAPSHOT_RUN_CLEANUP_NAME="$snapshot"
	trap cleanup_snapshot_run EXIT
	revert_all "$snapshot"
	wait_topology_ssh
	install_v2
	run_flow_by_name "$flow"
	revert_all "$snapshot"
	wait_topology_ssh
	reset_guided_lab_state
	SNAPSHOT_RUN_CLEANUP_ACTIVE=0
	SNAPSHOT_RUN_CLEANUP_NAME=""
	trap - EXIT
	pass "snapshot_flow_passed snapshot=$snapshot flow=$flow"
}

snapshot_run_all() {
	local snapshot="$1"
	local flow
	for flow in execute-rollback interrupted-resume post-start-rollback bad-ssh v2-start-failure runtime-guards real-activity failure-gates; do
		snapshot_run "$snapshot" "$flow"
	done
	pass "snapshot_all_flows_passed snapshot=$snapshot"
}

prepare_topology() {
	seed_db
	install_v1_sim
	install_v2
	migrate_v2
	smoke_preflight
	smoke_guided_dry_run
	pass "topology_prepared prefix=$PREFIX bucket_range=$LAB_BUCKET_MIN-$LAB_BUCKET_MAX"
}

shutdown_vm() {
	local vm="$1"
	if ! virsh_cmd dominfo "$vm" >/dev/null 2>&1; then
		return 0
	fi
	if [[ "$(virsh_cmd domstate "$vm")" == "running" ]]; then
		virsh_cmd shutdown "$vm" >/dev/null || true
		for _ in {1..36}; do
			[[ "$(virsh_cmd domstate "$vm" 2>/dev/null || true)" != "running" ]] && return 0
			sleep 5
		done
		virsh_cmd destroy "$vm" >/dev/null || true
	fi
}

snapshot_vm() {
	local vm="$1"
	local snapshot="$2"
	virsh_cmd dominfo "$vm" >/dev/null
	shutdown_vm "$vm"
	virsh_cmd snapshot-create-as "$vm" "$snapshot" "jetmon rollout lab snapshot $snapshot" --atomic >/dev/null
	pass "snapshot_created vm=$vm snapshot=$snapshot"
}

snapshot_all() {
	local snapshot="$1"
	snapshot_vm "$(vm_name db)" "$snapshot"
	snapshot_vm "$(vm_name v1)" "$snapshot"
	snapshot_vm "$(vm_name v2)" "$snapshot"
}

revert_vm() {
	local vm="$1"
	local snapshot="$2"
	virsh_cmd dominfo "$vm" >/dev/null
	shutdown_vm "$vm"
	virsh_cmd snapshot-revert "$vm" "$snapshot" >/dev/null
	virsh_cmd start "$vm" >/dev/null
	pass "snapshot_reverted vm=$vm snapshot=$snapshot"
}

revert_all() {
	local snapshot="$1"
	revert_vm "$(vm_name db)" "$snapshot"
	revert_vm "$(vm_name v1)" "$snapshot"
	revert_vm "$(vm_name v2)" "$snapshot"
}

destroy_vm() {
	local vm="$1"
	shutdown_vm "$vm"
	if virsh_cmd dominfo "$vm" >/dev/null 2>&1; then
		virsh_cmd undefine "$vm" --remove-all-storage --snapshots-metadata >/dev/null || virsh_cmd undefine "$vm" --snapshots-metadata >/dev/null
	fi
	rm -f "$(seed_path "$vm")" "$(user_data_path "$vm")" "$(meta_data_path "$vm")"
	pass "vm_destroyed=$vm"
}

destroy_topology() {
	destroy_vm "$(vm_name v2)"
	destroy_vm "$(vm_name v1)"
	destroy_vm "$(vm_name db)"
}

list_lab() {
	printf '## domains\n'
	virsh_cmd list --all | sed -n "1,2p; /$PREFIX-/p"
	printf '\n## leases\n'
	virsh_cmd net-dhcp-leases "$NETWORK" | sed -n "1,2p; /$PREFIX-/p"
	printf '\n## volumes\n'
	virsh_cmd vol-list "$POOL" | sed -n "1,2p; /$PREFIX-/p"
}

main() {
	local cmd="${1:-}"
	[[ -n "$cmd" ]] || {
		usage
		exit 2
	}
	shift || true
	case "$cmd" in
	doctor) doctor "$@" ;;
	fetch-image) fetch_image "$@" ;;
	create)
		[[ $# -ge 1 ]] || fail "create requires role"
		create_vm "$@"
		;;
	create-topology) create_topology "$@" ;;
	seed-db) seed_db "$@" ;;
	install-v1-sim) install_v1_sim "$@" ;;
	install-v2) install_v2 "$@" ;;
	migrate-v2) migrate_v2 "$@" ;;
	prepare-topology) prepare_topology "$@" ;;
	smoke-preflight) smoke_preflight "$@" ;;
	smoke-guided-dry-run) smoke_guided_dry_run "$@" ;;
	smoke-guided-execute-rollback) smoke_guided_execute_rollback "$@" ;;
	smoke-failure-gates) smoke_failure_gates "$@" ;;
	smoke-interrupted-resume) smoke_interrupted_resume "$@" ;;
	smoke-post-start-rollback) smoke_post_start_rollback "$@" ;;
	smoke-bad-ssh) smoke_bad_ssh "$@" ;;
	smoke-v2-start-failure) smoke_v2_start_failure "$@" ;;
	smoke-runtime-guards) smoke_runtime_guards "$@" ;;
	smoke-real-activity) smoke_real_activity "$@" ;;
	snapshot-run)
		[[ $# -eq 2 ]] || fail "snapshot-run requires snapshot and flow"
		snapshot_run "$@"
		;;
	snapshot-run-all)
		[[ $# -eq 1 ]] || fail "snapshot-run-all requires snapshot"
		snapshot_run_all "$1"
		;;
	wait-ssh)
		[[ $# -eq 1 ]] || fail "wait-ssh requires vm"
		wait_ssh "$(vm_name "$1")"
		;;
	ssh)
		[[ $# -ge 1 ]] || fail "ssh requires vm"
		vm="$(vm_name "$1")"
		shift
		ssh_vm "$vm" "$@"
		;;
	snapshot)
		[[ $# -eq 2 ]] || fail "snapshot requires vm and snapshot name"
		snapshot_vm "$(vm_name "$1")" "$2"
		;;
	snapshot-all)
		[[ $# -eq 1 ]] || fail "snapshot-all requires snapshot name"
		snapshot_all "$1"
		;;
	revert)
		[[ $# -eq 2 ]] || fail "revert requires vm and snapshot name"
		revert_vm "$(vm_name "$1")" "$2"
		;;
	revert-all)
		[[ $# -eq 1 ]] || fail "revert-all requires snapshot name"
		revert_all "$1"
		;;
	destroy)
		[[ $# -eq 1 ]] || fail "destroy requires vm"
		destroy_vm "$(vm_name "$1")"
		;;
	destroy-topology) destroy_topology "$@" ;;
	list) list_lab "$@" ;;
	-h | --help | help) usage ;;
	*) usage; fail "unknown command: $cmd" ;;
	esac
}

main "$@"
