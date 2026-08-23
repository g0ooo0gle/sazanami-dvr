#!/bin/sh
set -eu

baseline_commit=0b90ee4d5cdd137c23cdb966649e436db0170ea8
baseline_version=0.5.0
service_name=sazanami-dvr.service
install_root=/opt/sazanami-dvr
binary_link=/usr/local/bin/sazanami-dvr
config_root=/etc/sazanami-dvr
data_root=/var/lib/sazanami-dvr
unit_link=/etc/systemd/system/sazanami-dvr.service
wants_link=/etc/systemd/system/multi-user.target.wants/sazanami-dvr.service
provider_port=40772
provider_url=http://127.0.0.1:40772
provider_pid=
work_root=
owned_resources=0
unit_owned=0
completed=0

fail() {
  printf 'Linux lifecycle確認に失敗しました: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required-command-missing:$1"
}

require_absent() {
  if [ -e "$1" ] || [ -L "$1" ]; then
    fail "existing-resource:$1"
  fi
}

unit_link_targets_release() {
  link_path=$1
  release_root=$2
  [ -L "$link_path" ] || return 1
  link_target=$(readlink "$link_path") || return 1
  case "$link_target" in
    "$release_root"/*/packaging/systemd/sazanami-dvr.service) return 0 ;;
    *) return 1 ;;
  esac
}

check_purge_target() {
  case "${1:-}" in
    /opt/sazanami-dvr | /etc/sazanami-dvr | /var/lib/sazanami-dvr) ;;
    *) fail "purge-target-outside-fixed-scope" ;;
  esac
}

safe_remove_tree() {
  check_purge_target "$1"
  [ ! -L "$1" ] || fail "purge-target-is-symlink:$1"
  if [ -e "$1" ]; then
    [ -d "$1" ] || fail "purge-target-is-not-directory:$1"
    rm -rf -- "$1"
  fi
}

cleanup_tree() {
  check_purge_target "$1"
  if [ -L "$1" ] || [ -f "$1" ]; then
    rm -f -- "$1"
  elif [ -d "$1" ]; then
    rm -rf -- "$1"
  fi
}

remove_work_root() {
  case "${work_root:-}" in
    /run/sazanami-dvr-lifecycle.[A-Za-z0-9]*) ;;
    '') return ;;
    *) fail "work-root-outside-fixed-scope" ;;
  esac
  [ ! -L "$work_root" ] || fail "work-root-is-symlink"
  if [ -d "$work_root" ]; then
    rm -rf -- "$work_root"
  fi
  work_root=
}

port_is_free() {
  ! ss -H -ltn "sport = :$1" | grep -q .
}

preflight() {
  [ "$(id -u)" -eq 0 ] || fail "root-required"
  [ "$(uname -s)" = Linux ] || fail "linux-required"
  [ "$(uname -m)" = x86_64 ] || fail "linux-amd64-required"
  for command_name in awk chown chmod cmp find getent go grep groupdel install ln mktemp python3 readlink rm runuser sed sort ss stat systemctl tar timeout useradd userdel; do
    require_command "$command_name"
  done
  if getent passwd sazanami-dvr >/dev/null; then
    fail "existing-user:sazanami-dvr"
  fi
  if getent group sazanami-dvr >/dev/null; then
    fail "existing-group:sazanami-dvr"
  fi
  for resource in "$install_root" "$binary_link" "$config_root" "$data_root" "$unit_link" "$wants_link"; do
    require_absent "$resource"
  done
  for resource in \
    "/run/systemd/system/$service_name" \
    "/run/systemd/transient/$service_name" \
    "/usr/local/lib/systemd/system/$service_name" \
    "/usr/lib/systemd/system/$service_name" \
    "/lib/systemd/system/$service_name" \
    "/etc/systemd/system/$service_name.d" \
    "/run/systemd/system/$service_name.d" \
    "/usr/local/lib/systemd/system/$service_name.d" \
    "/usr/lib/systemd/system/$service_name.d" \
    "/lib/systemd/system/$service_name.d"; do
    require_absent "$resource"
  done
  load_state=$(systemctl show "$service_name" --property=LoadState --value 2>/dev/null) ||
    fail "systemd-unavailable"
  [ "$load_state" = not-found ] || fail "existing-unit-loaded:$service_name:$load_state"
  drop_in_paths=$(systemctl show "$service_name" --property=DropInPaths --value 2>/dev/null) ||
    fail "systemd-unavailable"
  [ -z "$drop_in_paths" ] || fail "existing-unit-drop-in:$service_name"
  for port in 4520 4521 4522 "$provider_port"; do
    port_is_free "$port" || fail "port-in-use:$port"
  done
}

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if [ -n "$provider_pid" ]; then
    kill "$provider_pid" 2>/dev/null || true
    wait "$provider_pid" 2>/dev/null || true
  fi
  if [ "$owned_resources" -eq 1 ] && [ "$completed" -ne 1 ]; then
    if [ "$unit_owned" -eq 1 ] && unit_link_targets_release "$unit_link" "$install_root"; then
      systemctl disable --now "$service_name" >/dev/null 2>&1 || true
      rm -f -- "$unit_link" "$wants_link"
    fi
    rm -f -- "$binary_link"
    systemctl daemon-reload >/dev/null 2>&1 || true
    cleanup_tree "$install_root"
    cleanup_tree "$config_root"
    cleanup_tree "$data_root"
    if getent passwd sazanami-dvr >/dev/null; then
      userdel sazanami-dvr >/dev/null 2>&1 || true
    fi
    if getent group sazanami-dvr >/dev/null; then
      groupdel sazanami-dvr >/dev/null 2>&1 || true
    fi
  fi
  remove_work_root
  exit "$result"
}

validate_archive() {
  archive=$1
  expected_commit=$2
  expected_version=$3
  changelog_required=$4
  list_file=$5
  extract_root=$6

  case "$archive" in
    /*) ;;
    *) fail "archive-path-must-be-absolute" ;;
  esac
  [ -f "$archive" ] && [ ! -L "$archive" ] || fail "archive-must-be-regular"
  tar -tzf "$archive" > "$list_file"
  tar -tvzf "$archive" | awk '{ type = substr($1, 1, 1); if (type != "-" && type != "d") exit 1 }' ||
    fail "archive-link-or-special-file"
  package=$(sed -n '1s,/.*,,p' "$list_file")
  printf '%s\n' "$package" | grep -Eq '^sazanami-dvr_[0-9]+\.[0-9]+\.[0-9]+_linux_amd64$' ||
    fail "archive-root-invalid"
  if grep -Eq '(^|/)\.\.(/|$)|^/' "$list_file"; then
    fail "archive-path-invalid"
  fi
  while IFS= read -r archive_entry; do
    case "$archive_entry" in
      "$package" | "$package"/*) ;;
      *) fail "archive-has-multiple-roots" ;;
    esac
  done < "$list_file"

  required_files='sazanami-dvr LICENSE README.md THIRD_PARTY_NOTICES.md docs/linux-installation.md packaging/systemd/sazanami-dvr.service packaging/systemd/sazanami-dvr.env.example'
  if [ "$changelog_required" -eq 1 ]; then
    required_files="$required_files CHANGELOG.md"
  fi
  for required_file in $required_files; do
    grep -Fx "$package/$required_file" "$list_file" >/dev/null || fail "archive-file-missing:$required_file"
  done

  install -d -o root -g root -m 0755 "$extract_root"
  tar -xzf "$archive" -C "$extract_root" --strip-components=1
  chown -R root:root "$extract_root"
  binary="$extract_root/sazanami-dvr"
  [ -x "$binary" ] && [ ! -L "$binary" ] || fail "archive-binary-invalid"
  [ "$($binary --version)" = "sazanami-dvr $expected_version" ] || fail "archive-version-mismatch"
  build_info=$(go version -m "$binary")
  for required_setting in \
    "vcs.revision=$expected_commit" \
    "vcs.modified=false" \
    "CGO_ENABLED=0" \
    "GOOS=linux" \
    "GOARCH=amd64"; do
    printf '%s\n' "$build_info" | grep -Fq "$required_setting" || fail "archive-identity-mismatch:$required_setting"
  done
}

start_provider() {
  python3 "$1/packaging/lifecycle/synthetic_mirakurun.py" "$provider_port" &
  provider_pid=$!
  timeout 10 sh -c 'until python3 -c '\''import urllib.request; urllib.request.urlopen("http://127.0.0.1:40772/api/version", timeout=1).read()'\''; do sleep 0.1; done'
}

run_as_service() {
  runuser -u sazanami-dvr -- "$@"
}

ensure_current() {
  database_binary=$1
  status_output=$(run_as_service "$database_binary" db status --data-root "$data_root")
  case "$status_output" in
    state=CURRENT*) return 0 ;;
    state=BEHIND*)
      run_as_service "$database_binary" db migrate --data-root "$data_root"
      status_output=$(run_as_service "$database_binary" db status --data-root "$data_root")
      case "$status_output" in
        state=CURRENT*) return 0 ;;
      esac
      ;;
  esac
  fail "database-not-current"
}

switch_release() {
  release_root=$1
  ln -sfn "$release_root/sazanami-dvr" "$binary_link"
  ln -sfn "$release_root/packaging/systemd/sazanami-dvr.service" "$unit_link"
  unit_link_targets_release "$unit_link" "$install_root" || fail "unit-link-outside-install-root"
  unit_owned=1
  systemctl daemon-reload
}

wait_for_service() {
  systemctl start "$service_name"
  timeout 15 sh -c 'until systemctl is-active --quiet sazanami-dvr.service && ss -H -ltn "sport = :4520" | grep -q . && ss -H -ltn "sport = :4521" | grep -q .; do sleep 0.2; done'
  systemctl is-active --quiet "$service_name"
}

snapshot_retained_data() {
  snapshot_file=$1
  for retained_path in \
    "$config_root" \
    "$config_root/sazanami-dvr.env" \
    "$data_root" \
    "$data_root/channels.json" \
    "$data_root/catalog.sqlite3" \
    "$data_root/backups" \
    "$data_root/recordings"; do
    [ -e "$retained_path" ] && [ ! -L "$retained_path" ] || fail "retained-path-missing:$retained_path"
    stat -c '%n|%F|%U|%G|%a|%s' "$retained_path"
  done > "$snapshot_file"
  find "$data_root" -mindepth 1 -printf '%P|%y|%u|%g|%m|%s\n' | sort >> "$snapshot_file"
}

main() {
  if [ "${1:-}" = --preflight-only ]; then
    [ "$#" -eq 1 ] || fail "usage"
    preflight
    return
  fi
  [ "$#" -eq 4 ] || fail "usage: verify.sh <v0.5.0-archive> <candidate-archive> <candidate-sha> <candidate-version>"
  baseline_archive=$1
  candidate_archive=$2
  candidate_commit=$3
  candidate_version=$4
  printf '%s\n' "$candidate_commit" | grep -Eq '^[0-9a-f]{40}$' || fail "candidate-commit-invalid"
  printf '%s\n' "$candidate_version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
    fail "candidate-version-invalid"

  preflight
  owned_resources=1
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  work_root=$(mktemp -d /run/sazanami-dvr-lifecycle.XXXXXX)
  baseline_root="$install_root/$baseline_version"
  candidate_root="$install_root/candidate-${candidate_commit%${candidate_commit#????????????}}"

  install -d -o root -g root -m 0755 "$install_root"
  validate_archive "$baseline_archive" "$baseline_commit" "$baseline_version" 0 \
    "$work_root/baseline.list" "$baseline_root"
  validate_archive "$candidate_archive" "$candidate_commit" "$candidate_version" 1 \
    "$work_root/candidate.list" "$candidate_root"
  start_provider "$candidate_root"

  useradd --system --user-group --home-dir "$data_root" --shell /usr/sbin/nologin sazanami-dvr
  install -d -o root -g sazanami-dvr -m 0750 "$config_root"
  install -d -o sazanami-dvr -g sazanami-dvr -m 0700 "$data_root" "$data_root/recordings"
  printf 'sazanami lifecycle retention\n' > "$work_root/retention-sentinel"
  install -o sazanami-dvr -g sazanami-dvr -m 0600 \
    "$work_root/retention-sentinel" "$data_root/recordings/.lifecycle-retention-sentinel"
  install -o root -g root -m 0600 \
    "$baseline_root/packaging/systemd/sazanami-dvr.env.example" "$config_root/sazanami-dvr.env"

  run_as_service "$baseline_root/sazanami-dvr" db migrate --data-root "$data_root"
  ensure_current "$baseline_root/sazanami-dvr"
  catalog_output=$(run_as_service "$baseline_root/sazanami-dvr" catalog sync \
    --data-root "$data_root" --provider mirakurun --base-url "$provider_url")
  backend_id=$(printf '%s\n' "$catalog_output" | sed -n 's/^backend_id=\([^ ]*\) .*/\1/p')
  printf '%s\n' "$backend_id" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' ||
    fail "backend-id-missing"
  python3 - "$backend_id" > "$data_root/channels.json" <<'PY'
import json
import sys

json.dump({
    "format": "sazanami-channel-map-v1",
    "backend_id": sys.argv[1],
    "services": [{
        "provider_locator": "100003",
        "network_id": 1,
        "service_id": 3,
        "transport_stream_id": 2,
        "provider_name": "",
        "network_name": "",
        "transport_stream_name": "",
        "remote_control_key_id": 1,
        "partial_reception": False,
        "epg_capture": True,
        "search": True,
    }],
}, sys.stdout, separators=(",", ":"))
PY
  chown root:sazanami-dvr "$data_root/channels.json"
  chmod 0640 "$data_root/channels.json"
  run_as_service "$baseline_root/sazanami-dvr" ctrlcmd validate \
    --data-root "$data_root" --channel-map "$data_root/channels.json"

  switch_release "$baseline_root"
  systemctl enable "$service_name"
  wait_for_service
  systemctl stop "$service_name"
  backup_output=$(run_as_service "$baseline_root/sazanami-dvr" db backup --data-root "$data_root")
  backup_id=$(printf '%s\n' "$backup_output" | sed -n 's/^backup_id=\([^ ]*\) state=complete .*/\1/p')
  printf '%s\n' "$backup_id" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' ||
    fail "backup-id-missing"

  ensure_current "$candidate_root/sazanami-dvr"
  switch_release "$candidate_root"
  wait_for_service
  systemctl stop "$service_name"
  restore_output=$(run_as_service "$candidate_root/sazanami-dvr" db restore \
    --data-root "$data_root" --backup-id "$backup_id")
  printf '%s\n' "$restore_output" | grep -Eq '^operation_id=[0-9a-f-]+ phase=COMMITTED$' || fail "restore-not-committed"

  ensure_current "$baseline_root/sazanami-dvr"
  switch_release "$baseline_root"
  wait_for_service
  systemctl stop "$service_name"
  ensure_current "$candidate_root/sazanami-dvr"
  switch_release "$candidate_root"
  wait_for_service
  systemctl stop "$service_name"

  snapshot_retained_data "$work_root/before-uninstall"
  unit_link_targets_release "$unit_link" "$install_root" || fail "unit-link-ownership-lost"
  systemctl disable "$service_name"
  rm -f -- "$unit_link" "$binary_link"
  systemctl daemon-reload
  safe_remove_tree "$install_root"
  require_absent "$unit_link"
  require_absent "$wants_link"
  require_absent "$binary_link"
  require_absent "$install_root"
  snapshot_retained_data "$work_root/after-uninstall"
  cmp -s "$work_root/before-uninstall" "$work_root/after-uninstall" || fail "retained-data-changed"
  cmp -s "$work_root/retention-sentinel" "$data_root/recordings/.lifecycle-retention-sentinel" ||
    fail "retained-recording-content-changed"
  getent passwd sazanami-dvr >/dev/null || fail "retained-user-missing"
  getent group sazanami-dvr >/dev/null || fail "retained-group-missing"

  safe_remove_tree "$config_root"
  safe_remove_tree "$data_root"
  userdel sazanami-dvr
  if getent group sazanami-dvr >/dev/null; then
    groupdel sazanami-dvr
  fi
  require_absent "$config_root"
  require_absent "$data_root"
  getent passwd sazanami-dvr >/dev/null && fail "purge-user-remains"
  getent group sazanami-dvr >/dev/null && fail "purge-group-remains"

  remove_work_root
  owned_resources=0
  completed=1
  printf 'Linux lifecycle確認を完了しました: baseline=%s candidate=%s\n' "$baseline_commit" "$candidate_commit"
}

if [ "${0##*/}" = verify.sh ]; then
  main "$@"
fi
