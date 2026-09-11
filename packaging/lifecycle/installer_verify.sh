#!/bin/sh
set -eu

service_name=sazanami-dvr.service
account_name=sazanami-dvr
peer_name=sazanami-installer-peer
install_root=/opt/sazanami-dvr
binary_link=/usr/local/bin/sazanami-dvr
unit_link=/etc/systemd/system/sazanami-dvr.service
wants_root=/etc/systemd/system/multi-user.target.wants
wants_link=/etc/systemd/system/multi-user.target.wants/sazanami-dvr.service
config_root=/etc/sazanami-dvr
data_root=/var/lib/sazanami-dvr
recording_root=/var/lib/sazanami-dvr/recordings
account_marker=$config_root/.installer-managed-account
runtime_marker=$install_root/.installer-managed-runtime
external_root=/srv/sazanami-dvr-installer-test
external_alias=/srv/sazanami-dvr-installer-alias
external_mount=/srv/sazanami-dvr-installer-mount
saved_data_root=/var/lib/sazanami-dvr.installer-test-real
test_drop_in=/run/systemd/system/sazanami-dvr.service.d/installer-test.conf
test_drop_in_root=/run/systemd/system/sazanami-dvr.service.d
unmanaged_wants_root=/etc/systemd/system/sazanami-installer-test.target.wants
unmanaged_wants_link=$unmanaged_wants_root/sazanami-dvr.service

conflict_resource_paths="
/run/systemd/system/$service_name
/run/systemd/transient/$service_name
/usr/local/lib/systemd/system/$service_name
/usr/lib/systemd/system/$service_name
/lib/systemd/system/$service_name
/etc/systemd/system/$service_name.d
/run/systemd/system/$service_name.d
/usr/local/lib/systemd/system/$service_name.d
/usr/lib/systemd/system/$service_name.d
/lib/systemd/system/$service_name.d
$wants_link
"

work_root=
installer=
candidate_version=
owned_resources=0
external_mount_active=0
data_mount_active=0
data_root_swapped=0
opt_metadata_changed=0
opt_uid=
opt_gid=
opt_mode=
account_runner_pid=
account_process_pid=
purge_pid=
usr_local_bin_metadata_changed=0
usr_local_bin_uid=
usr_local_bin_gid=
usr_local_bin_mode=
wants_root_mount_active=0
active_conflict_path=
conflict_created_dirs=
cleanup_safe=1

fail() {
  printf 'Installer lifecycle確認に失敗しました: %s\n' "$*" >&2
  exit 1
}

path_exists() {
  [ -e "$1" ] || [ -L "$1" ]
}

require_absent() {
  path_exists "$1" && fail "existing-resource:$1"
  return 0
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required-command-missing:$1"
}

stop_account_process() {
  if [ -n "$account_process_pid" ]; then
    kill "$account_process_pid" 2>/dev/null || true
    account_process_pid=
  fi
  if [ -n "$account_runner_pid" ]; then
    wait "$account_runner_pid" 2>/dev/null || true
    account_runner_pid=
  fi
}

restore_data_root() {
  if [ "$data_root_swapped" -eq 1 ]; then
    if [ -d "$saved_data_root" ] && [ ! -L "$saved_data_root" ]; then
      if [ -L "$data_root" ] && [ "$(readlink "$data_root")" = "$saved_data_root" ]; then
        rm -f -- "$data_root"
      elif path_exists "$data_root"; then
        printf 'Installer lifecycle cleanup: unexpected data root type: %s\n' "$data_root" >&2
        return 1
      fi
      mv "$saved_data_root" "$data_root" || return 1
    elif [ -d "$data_root" ] && [ ! -L "$data_root" ]; then
      data_root_swapped=0
      return 0
    else
      printf 'Installer lifecycle cleanup: data root recovery state is invalid\n' >&2
      return 1
    fi
    data_root_swapped=0
  fi
  return 0
}

cleanup_conflict_resource() {
  if [ -n "$active_conflict_path" ]; then
    case "$active_conflict_path" in
      *.d) rm -rf -- "$active_conflict_path" ;;
      *) rm -f -- "$active_conflict_path" ;;
    esac
    active_conflict_path=
  fi
  for conflict_created_dir in $conflict_created_dirs; do
    rmdir "$conflict_created_dir" 2>/dev/null || true
  done
  conflict_created_dirs=
}

restore_wants_root() {
  if [ "$wants_root_mount_active" -eq 1 ]; then
    if ! umount "$wants_root"; then
      printf 'Installer lifecycle cleanup: wants root mount remains: %s\n' "$wants_root" >&2
      return 1
    fi
    wants_root_mount_active=0
  fi
  return 0
}

cleanup() {
  result=$?
  trap - EXIT
  trap '' HUP INT TERM
  if [ -n "$purge_pid" ]; then
    kill "$purge_pid" 2>/dev/null || true
    wait "$purge_pid" 2>/dev/null || true
    purge_pid=
  fi
  stop_account_process
  cleanup_conflict_resource
  restore_wants_root || cleanup_safe=0
  if [ "$data_mount_active" -eq 1 ]; then
    if umount "$data_root"; then
      data_mount_active=0
    else
      cleanup_safe=0
    fi
  fi
  if [ "$external_mount_active" -eq 1 ]; then
    if umount "$external_mount"; then
      external_mount_active=0
    else
      cleanup_safe=0
    fi
  fi
  restore_data_root || cleanup_safe=0
  if [ "$opt_metadata_changed" -eq 1 ]; then
    chown "$opt_uid:$opt_gid" /opt 2>/dev/null || true
    chmod "$opt_mode" /opt 2>/dev/null || true
    opt_metadata_changed=0
  fi
  if [ "$usr_local_bin_metadata_changed" -eq 1 ]; then
    chown "$usr_local_bin_uid:$usr_local_bin_gid" /usr/local/bin 2>/dev/null || true
    chmod "$usr_local_bin_mode" /usr/local/bin 2>/dev/null || true
    usr_local_bin_metadata_changed=0
  fi
  if [ "$owned_resources" -eq 1 ] && [ "$cleanup_safe" -eq 1 ]; then
    systemctl stop "$service_name" >/dev/null 2>&1 || true
    systemctl disable "$service_name" >/dev/null 2>&1 || true
    rm -f -- "$test_drop_in"
    rmdir "$test_drop_in_root" 2>/dev/null || true
    rm -f -- "$unit_link" "$binary_link" "$wants_link" "$unmanaged_wants_link"
    rmdir "$unmanaged_wants_root" 2>/dev/null || true
    systemctl daemon-reload >/dev/null 2>&1 || true
    for cleanup_root in "$install_root" "$config_root" "$data_root" "$saved_data_root"; do
      if [ -L "$cleanup_root" ]; then
        rm -f -- "$cleanup_root"
      elif [ -d "$cleanup_root" ]; then
        rm -rf -- "$cleanup_root"
      fi
    done
    if getent passwd "$peer_name" >/dev/null 2>&1; then
      userdel "$peer_name" >/dev/null 2>&1 || true
    fi
    if getent group "$peer_name" >/dev/null 2>&1; then
      groupdel "$peer_name" >/dev/null 2>&1 || true
    fi
    if getent passwd "$account_name" >/dev/null 2>&1; then
      userdel "$account_name" >/dev/null 2>&1 || true
    fi
    if getent group "$account_name" >/dev/null 2>&1; then
      groupdel "$account_name" >/dev/null 2>&1 || true
    fi
    for cleanup_external in "$external_alias" "$external_mount" "$external_root"; do
      if [ -L "$cleanup_external" ]; then
        rm -f -- "$cleanup_external"
      elif [ -d "$cleanup_external" ]; then
        rm -rf -- "$cleanup_external"
      fi
    done
  fi
  if [ "$cleanup_safe" -eq 1 ] && [ -n "$work_root" ]; then
    case "$work_root" in
      /run/sazanami-installer-lifecycle.*)
        [ ! -L "$work_root" ] && rm -rf -- "$work_root"
        ;;
    esac
  fi
  if [ "$cleanup_safe" -ne 1 ]; then
    printf 'Installer lifecycle cleanupを安全に完了できなかったため、試験資源を保持しました。\n' >&2
    result=1
  fi
  exit "$result"
}

preflight() {
  [ "$(id -u)" -eq 0 ] || fail root-required
  [ "$(uname -s)" = Linux ] || fail linux-required
  for command_name in awk chmod chown cmp cp find getent grep groupadd groupdel install ln mkfifo mktemp mount mv pgrep readlink rm rmdir runuser sed sh sleep sort stat systemctl tar timeout touch umount uniq useradd userdel usermod; do
    require_command "$command_name"
  done
  [ -d /run/systemd/system ] || fail systemd-required
  for resource in "$install_root" "$binary_link" "$unit_link" "$wants_link" "$config_root" "$data_root" \
    "$saved_data_root" "$external_root" "$external_alias" "$external_mount" "$test_drop_in_root" \
    "$unmanaged_wants_root"; do
    require_absent "$resource"
  done
  for conflict_resource in $conflict_resource_paths; do
    require_absent "$conflict_resource"
  done
  getent passwd "$account_name" >/dev/null 2>&1 && fail existing-account
  getent group "$account_name" >/dev/null 2>&1 && fail existing-group
  getent passwd "$peer_name" >/dev/null 2>&1 && fail existing-peer
  getent group "$peer_name" >/dev/null 2>&1 && fail existing-peer-group
  [ -d /opt ] && [ ! -L /opt ] || fail unsafe-opt-path
  [ -d /usr/local/bin ] && [ ! -L /usr/local/bin ] || fail unsafe-usr-local-bin-path
  opt_uid=$(stat -c %u /opt)
  opt_gid=$(stat -c %g /opt)
  opt_mode=$(stat -c %a /opt)
  [ "$opt_uid" -eq 0 ] || fail unsafe-opt-owner
  usr_local_bin_uid=$(stat -c %u /usr/local/bin)
  usr_local_bin_gid=$(stat -c %g /usr/local/bin)
  usr_local_bin_mode=$(stat -c %a /usr/local/bin)
}

ensure_conflict_directory() {
  if path_exists "$1"; then
    [ -d "$1" ] && [ ! -L "$1" ] || fail conflict-parent-invalid
    return 0
  fi
  conflict_parent=${1%/*}
  [ "$conflict_parent" != "$1" ] || fail conflict-parent-invalid
  ensure_conflict_directory "$conflict_parent"
  install -d -o root -g root -m 0755 "$1"
  conflict_created_dirs="$1 $conflict_created_dirs"
}

create_conflict_resource() {
  conflict_path=$1
  require_absent "$conflict_path"
  case "$conflict_path" in
    "$wants_link")
      conflict_parent=${conflict_path%/*}
      ensure_conflict_directory "$conflict_parent"
      ln -s "$unit_link" "$conflict_path"
      ;;
    *.d)
      ensure_conflict_directory "$conflict_path"
      ;;
    *)
      conflict_parent=${conflict_path%/*}
      ensure_conflict_directory "$conflict_parent"
      install -o root -g root -m 0644 /dev/null "$conflict_path"
      ;;
  esac
  active_conflict_path=$conflict_path
}

wants_root_is_mountpoint() {
  awk -v path="$wants_root" '$5 == path { found = 1 } END { exit found ? 0 : 1 }' /proc/self/mountinfo
}

test_wants_root_bind_mount() {
  [ -d "$wants_root" ] && [ ! -L "$wants_root" ] || fail wants-root-invalid
  wants_root_is_mountpoint && fail wants-root-already-mounted
  wants_root_mount_active=1
  if mount --bind "$wants_root" "$wants_root"; then
    expect_rejected wants-root-bind-mount uninstall unsafe-standard-path
    [ -L "$wants_link" ] || fail wants-root-bind-link-removed
    restore_wants_root || fail wants-root-bind-unmount
  else
    wants_root_mount_active=0
    printf 'Installer lifecycle確認: wants root bind mount testをskipしました（mount不可）\n' >&2
  fi
}

validate_candidate_archive() {
  candidate_archive_path=$1
  candidate_archive_version=$2
  candidate_archive_list=$work_root/candidate-archive.list

  [ -f "$candidate_archive_path" ] && [ ! -L "$candidate_archive_path" ] || fail candidate-archive-invalid
  tar -tzf "$candidate_archive_path" > "$candidate_archive_list" || fail candidate-archive-invalid
  tar -tvzf "$candidate_archive_path" |
    awk '{ type = substr($1, 1, 1); if (type != "-" && type != "d") exit 1 }' ||
    fail candidate-archive-special-entry

  candidate_package=$(sed -n '1s,/.*,,p' "$candidate_archive_list")
  [ "$candidate_package" = "sazanami-dvr_${candidate_archive_version}_linux_amd64" ] ||
    fail candidate-archive-root-invalid
  if grep -Eq '(^|/)\.\.(/|$)|^/' "$candidate_archive_list"; then
    fail candidate-archive-path-invalid
  fi
  while IFS= read -r candidate_entry; do
    case "$candidate_entry" in
      "$candidate_package" | "$candidate_package"/*) ;;
      *) fail candidate-archive-multiple-roots ;;
    esac
  done < "$candidate_archive_list"
  duplicate_entry=$(sort "$candidate_archive_list" | uniq -d | sed -n '1p')
  [ -z "$duplicate_entry" ] || fail candidate-archive-duplicate-entry

  for candidate_required in \
    sazanami-dvr \
    README.md \
    CHANGELOG.md \
    docs/linux-installation.md \
    docs/docker-compose.md \
    packaging/install.sh \
    packaging/systemd/sazanami-dvr.service \
    packaging/systemd/sazanami-dvr.env.example; do
    grep -Fx "$candidate_package/$candidate_required" "$candidate_archive_list" >/dev/null ||
      fail "candidate-archive-file-missing:$candidate_required"
  done
}

assert_runtime_present() {
  release_root=$install_root/$candidate_version
  [ -d "$release_root" ] && [ ! -L "$release_root" ] || fail runtime-root-missing
  [ -x "$release_root/sazanami-dvr" ] && [ ! -L "$release_root/sazanami-dvr" ] || fail runtime-binary-missing
  [ -x "$release_root/packaging/install.sh" ] && [ ! -L "$release_root/packaging/install.sh" ] ||
    fail runtime-installer-missing
  [ -L "$binary_link" ] && [ "$(readlink "$binary_link")" = "$release_root/sazanami-dvr" ] || fail binary-link-invalid
  [ -L "$unit_link" ] &&
    [ "$(readlink "$unit_link")" = "$release_root/packaging/systemd/sazanami-dvr.service" ] || fail unit-link-invalid
  [ -f "$runtime_marker" ] && [ ! -L "$runtime_marker" ] || fail runtime-marker-missing
  [ "$(stat -c %u:%g:%a:%h "$runtime_marker")" = 0:0:600:1 ] || fail runtime-marker-metadata
  grep -qx 'identifier=sazanami-dvr-installer' "$runtime_marker" || fail runtime-marker-identifier
  grep -qx "version=$candidate_version" "$runtime_marker" || fail runtime-marker-version
}

assert_account_present() {
  [ -f "$account_marker" ] && [ ! -L "$account_marker" ] || fail account-marker-missing
  [ "$(stat -c %u:%g:%a:%h "$account_marker")" = 0:0:600:1 ] || fail account-marker-metadata
  marker_id=$(sed -n 's/^installation_id=//p' "$account_marker")
  marker_uid=$(sed -n 's/^uid=//p' "$account_marker")
  marker_gid=$(sed -n 's/^gid=//p' "$account_marker")
  printf '%s\n' "$marker_id" | grep -Eq '^[0-9a-f]{32,}$' || fail account-marker-id
  account_entry=$(getent passwd "$account_name") || fail account-missing
  group_entry=$(getent group "$account_name") || fail group-missing
  [ "$(printf '%s\n' "$account_entry" | awk -F: '{ print $3 ":" $4 ":" $5 ":" $6 ":" $7 }')" = \
    "$marker_uid:$marker_gid:sazanami-dvr-installer-$marker_id:$data_root:/usr/sbin/nologin" ] || fail account-identity
  [ "$(printf '%s\n' "$group_entry" | awk -F: '{ print $3 ":" $4 }')" = "$marker_gid:" ] || fail group-identity
}

assert_installed() {
  assert_runtime_present
  assert_account_present
  [ -d "$config_root" ] && [ "$(stat -c %u:%g:%a "$config_root")" = "0:$marker_gid:750" ] || fail config-root-metadata
  [ -d "$data_root" ] && [ "$(stat -c %u:%g:%a "$data_root")" = "$marker_uid:$marker_gid:700" ] || fail data-root-metadata
  [ -d "$recording_root" ] && [ "$(stat -c %u:%g:%a "$recording_root")" = "$marker_uid:$marker_gid:700" ] ||
    fail recording-root-metadata
  [ -f "$config_root/sazanami-dvr.env" ] && [ ! -L "$config_root/sazanami-dvr.env" ] || fail env-missing
  [ "$(stat -c %u:%g:%a:%h "$config_root/sazanami-dvr.env")" = 0:0:600:1 ] || fail env-metadata
}

assert_no_auto_preparation() {
  data_entries=$(find "$data_root" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
  [ "$data_entries" = recordings ] || fail automatic-data-created
  config_entries=$(find "$config_root" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
  [ "$config_entries" = '.installer-managed-account
sazanami-dvr.env' ] || fail automatic-config-created
  [ ! -L "$wants_link" ] || fail service-enabled
  if systemctl is-active --quiet "$service_name"; then
    fail service-started
  fi
}

snapshot_retained() {
  snapshot_file=$1
  {
    find "$config_root" "$data_root" -xdev -printf '%p|%y|%u|%g|%m|%s|%T@\n'
    getent passwd "$account_name"
    getent group "$account_name"
  } | sort > "$snapshot_file"
}

assert_purge_untouched() {
  assert_runtime_present
  assert_account_present
  [ -d "$config_root" ] && [ ! -L "$config_root" ] || fail config-root-changed
  [ -d "$data_root" ] && [ ! -L "$data_root" ] || fail data-root-changed
}

expect_rejected() {
  rejection_label=$1
  rejection_operation=$2
  rejection_reason=$3
  rejection_input=${4-}
  rejection_output=$work_root/rejected-$rejection_label.out
  if [ -n "$rejection_input" ]; then
    if printf '%s\n' "$rejection_input" | "$installer" "$rejection_operation" >"$rejection_output" 2>&1; then
      fail "rejected-operation-succeeded:$rejection_label"
    fi
  elif "$installer" "$rejection_operation" >"$rejection_output" 2>&1; then
    fail "rejected-operation-succeeded:$rejection_label"
  fi
  grep -F "$rejection_reason" "$rejection_output" >/dev/null || fail "rejection-reason-missing:$rejection_label"
}

test_managed_reinstall_conflicts() {
  conflict_index=0
  for conflict_resource in $conflict_resource_paths; do
    conflict_index=$((conflict_index + 1))
    create_conflict_resource "$conflict_resource"
    expect_rejected "managed-reinstall-conflict-$conflict_index" install existing-resource
    require_absent "$install_root"
    require_absent "$binary_link"
    require_absent "$unit_link"
    if [ "$conflict_resource" = "$wants_link" ]; then
      [ -L "$wants_link" ] || fail managed-reinstall-wants-conflict-removed
    else
      require_absent "$wants_link"
    fi
    assert_account_present
    cleanup_conflict_resource
  done
}

write_recording_root() {
  new_recording_root=$1
  sed "s|^SAZANAMI_RECORDING_ROOT=.*|SAZANAMI_RECORDING_ROOT=$new_recording_root|" \
    "$config_root/sazanami-dvr.env" > "$work_root/env.next"
  [ "$(grep -c '^SAZANAMI_RECORDING_ROOT=' "$work_root/env.next")" -eq 1 ] || fail env-rewrite-failed
  install -o root -g root -m 0600 "$work_root/env.next" "$config_root/sazanami-dvr.env"
}

wait_for_prompt() {
  prompt_output=$1
  prompt_pid=$2
  prompt_count=0
  while ! grep -F 'PURGEと入力してください' "$prompt_output" >/dev/null 2>&1; do
    kill -0 "$prompt_pid" 2>/dev/null || fail purge-prompt-process-exited
    prompt_count=$((prompt_count + 1))
    [ "$prompt_count" -lt 200 ] || fail purge-prompt-timeout
    sleep 0.05
  done
}

main() {
  [ "$#" -eq 2 ] || fail 'usage: installer_verify.sh <candidate-archive> <candidate-version>'
  candidate_archive=$1
  candidate_version=$2
  case "$candidate_archive" in
    /*) ;;
    *) fail archive-path-must-be-absolute ;;
  esac
  printf '%s\n' "$candidate_version" |
    grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail candidate-version-invalid

  preflight
  owned_resources=1
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  work_root=$(mktemp -d /run/sazanami-installer-lifecycle.XXXXXX)
  candidate_archive_copy=$work_root/candidate.tar.gz
  install -o root -g root -m 0600 "$candidate_archive" "$candidate_archive_copy"
  candidate_archive=$candidate_archive_copy
  validate_candidate_archive "$candidate_archive" "$candidate_version"
  archive_root=$work_root/archive
  install -d -o root -g root -m 0755 "$archive_root"
  tar -xzf "$candidate_archive" -C "$archive_root" --strip-components=1
  installer=$archive_root/packaging/install.sh
  [ -f "$installer" ] && [ ! -L "$installer" ] && [ -x "$installer" ] || fail installer-invalid

  install -d -o root -g root -m 0755 "$test_drop_in_root"
  printf '[Service]\n' > "$test_drop_in"
  chmod 0644 "$test_drop_in"
  if "$installer" install > "$work_root/fresh-unit-conflict.out" 2>&1; then
    fail fresh-unit-conflict-accepted
  fi
  if ! grep -F existing-resource "$work_root/fresh-unit-conflict.out" >/dev/null; then
    sed 's/^/installer output: /' "$work_root/fresh-unit-conflict.out" >&2
    fail fresh-unit-conflict-reason
  fi
  require_absent "$install_root"
  require_absent "$config_root"
  require_absent "$data_root"
  getent passwd "$account_name" >/dev/null 2>&1 && fail fresh-unit-conflict-created-account
  getent group "$account_name" >/dev/null 2>&1 && fail fresh-unit-conflict-created-group
  rm -f -- "$test_drop_in"
  rmdir "$test_drop_in_root"
  systemctl daemon-reload

  version_probe_sentinel=$work_root/version-probe-ran-as-root
  {
    printf '#!/bin/sh\n'
    printf 'if [ "$(id -u)" -eq 0 ]; then : > %s; fi\n' "$version_probe_sentinel"
    printf 'printf "sazanami-dvr %s\\n"\n' "$candidate_version"
  } > "$archive_root/sazanami-dvr"
  chmod 0755 "$archive_root/sazanami-dvr"
  "$installer" install
  [ ! -e "$version_probe_sentinel" ] || fail version-probe-ran-as-root
  "$installer" uninstall
  tar -xzf "$candidate_archive" -C "$archive_root" --strip-components=1

  "$installer" install
  assert_installed
  [ "$(runuser -u "$account_name" -- "$install_root/$candidate_version/sazanami-dvr" --version)" = \
    "sazanami-dvr $candidate_version" ] || fail installed-binary-version-mismatch
  assert_no_auto_preparation
  snapshot_retained "$work_root/first-install"
  "$installer" install
  snapshot_retained "$work_root/reinstall"
  cmp -s "$work_root/first-install" "$work_root/reinstall" || fail same-version-reinstall-changed-data
  assert_no_auto_preparation

  printf 'channel-map-sentinel\n' > "$work_root/channel-map"
  printf 'database-sentinel\n' > "$work_root/database"
  printf 'backup-sentinel\n' > "$work_root/backup"
  printf 'recording-sentinel\n' > "$work_root/recording"
  install -o root -g "$marker_gid" -m 0640 "$work_root/channel-map" "$data_root/channels.json"
  install -o "$marker_uid" -g "$marker_gid" -m 0600 "$work_root/database" "$data_root/catalog.sqlite3"
  install -d -o "$marker_uid" -g "$marker_gid" -m 0700 "$data_root/backups"
  install -o "$marker_uid" -g "$marker_gid" -m 0600 "$work_root/backup" "$data_root/backups/installer-test.backup"
  install -o "$marker_uid" -g "$marker_gid" -m 0600 "$work_root/recording" "$recording_root/installer-test.ts"
  snapshot_retained "$work_root/before-uninstall"
  cp "$config_root/sazanami-dvr.env" "$work_root/env.before-uninstall"
  cp "$account_marker" "$work_root/marker.before-uninstall"
  systemctl enable "$service_name" >/dev/null
  [ -L "$wants_link" ] || fail service-enable-missing
  test_wants_root_bind_mount
  install -d -o root -g root -m 0755 "$unmanaged_wants_root"
  ln -s "$unit_link" "$unmanaged_wants_link"

  "$installer" uninstall
  require_absent "$install_root"
  require_absent "$binary_link"
  require_absent "$unit_link"
  require_absent "$wants_link"
  [ -L "$unmanaged_wants_link" ] || fail uninstall-removed-unmanaged-systemd-link
  [ "$(readlink "$unmanaged_wants_link")" = "$unit_link" ] || fail unmanaged-systemd-link-changed
  rm -f -- "$unmanaged_wants_link"
  rmdir "$unmanaged_wants_root"
  snapshot_retained "$work_root/after-uninstall"
  cmp -s "$work_root/before-uninstall" "$work_root/after-uninstall" || fail uninstall-changed-retained-data
  cmp -s "$work_root/env.before-uninstall" "$config_root/sazanami-dvr.env" || fail uninstall-changed-env
  cmp -s "$work_root/marker.before-uninstall" "$account_marker" || fail uninstall-changed-account-marker
  cmp -s "$work_root/channel-map" "$data_root/channels.json" || fail uninstall-changed-channel-map
  cmp -s "$work_root/database" "$data_root/catalog.sqlite3" || fail uninstall-changed-database
  cmp -s "$work_root/backup" "$data_root/backups/installer-test.backup" || fail uninstall-changed-backup
  cmp -s "$work_root/recording" "$recording_root/installer-test.ts" || fail uninstall-changed-recording
  assert_account_present

  test_managed_reinstall_conflicts

  mv "$recording_root" "$work_root/recordings.saved"
  ln -s "$work_root/recordings.saved" "$recording_root"
  expect_rejected managed-recording-symlink install existing-resource
  rm -f -- "$recording_root"
  mv "$work_root/recordings.saved" "$recording_root"

  "$installer" install
  assert_installed
  systemctl enable "$service_name" >/dev/null
  [ -L "$wants_link" ] || fail service-enable-missing-after-reinstall

  install -d -o root -g root -m 0755 "$test_drop_in_root"
  printf '[Service]\nExecStart=\nExecStart=/usr/bin/sleep 300\n' > "$test_drop_in"
  chmod 0644 "$test_drop_in"
  systemctl daemon-reload
  systemctl start "$service_name"
  timeout 10 sh -c 'until systemctl is-active --quiet sazanami-dvr.service; do sleep 0.1; done'
  expect_rejected active-service uninstall service-active
  assert_purge_untouched
  systemctl stop "$service_name"
  rm -f -- "$test_drop_in"
  rmdir "$test_drop_in_root"
  systemctl daemon-reload

  cp "$account_marker" "$work_root/account-marker.saved"
  rm -f -- "$account_marker"
  expect_rejected missing-account-marker install account-not-managed
  assert_runtime_present
  install -o root -g root -m 0600 "$work_root/account-marker.saved" "$account_marker"
  assert_account_present

  release_root=$install_root/$candidate_version
  touch "$release_root/unknown-installer-test"
  expect_rejected unknown-runtime uninstall runtime-not-managed
  assert_purge_untouched
  rm -f -- "$release_root/unknown-installer-test"

  expect_rejected purge-cancel purge purge-confirmation-required CANCEL
  assert_purge_untouched
  [ -L "$wants_link" ] || fail purge-cancel-disabled-service

  usermod --comment sazanami-dvr-installer-wrong "$account_name"
  expect_rejected account-comment purge account-not-managed PURGE
  assert_runtime_present
  usermod --comment "sazanami-dvr-installer-$marker_id" "$account_name"
  assert_account_present

  useradd --system --no-user-group --no-create-home --gid "$marker_gid" \
    --home-dir /nonexistent --shell /usr/sbin/nologin "$peer_name"
  expect_rejected shared-primary-group purge account-not-managed PURGE
  assert_purge_untouched
  userdel "$peer_name"

  runuser -u "$account_name" -- sleep 300 &
  account_runner_pid=$!
  account_process_pid=
  process_wait_count=0
  while [ -z "$account_process_pid" ]; do
    account_process_pid=$(pgrep -u "$marker_uid" | sed -n '1p' || true)
    process_wait_count=$((process_wait_count + 1))
    [ "$process_wait_count" -lt 100 ] || fail account-process-start-timeout
    [ -n "$account_process_pid" ] || sleep 0.05
  done
  expect_rejected account-process purge account-has-processes PURGE
  assert_purge_untouched
  stop_account_process

  cp "$config_root/sazanami-dvr.env" "$work_root/env.valid"
  printf 'SAZANAMI_DATA_ROOT=/var/lib/sazanami-dvr\n' >> "$config_root/sazanami-dvr.env"
  expect_rejected duplicate-env purge purge-env-not-safe PURGE
  assert_purge_untouched
  install -o root -g root -m 0600 "$work_root/env.valid" "$config_root/sazanami-dvr.env"

  ln "$config_root/sazanami-dvr.env" "$config_root/env-hardlink-test"
  expect_rejected env-hardlink purge purge-env-not-safe PURGE
  assert_purge_untouched
  rm -f -- "$config_root/env-hardlink-test"

  ln -s "$work_root" "$data_root/symlink-test"
  expect_rejected data-symlink purge purge-path-not-safe PURGE
  assert_purge_untouched
  rm -f -- "$data_root/symlink-test"

  mkfifo "$data_root/fifo-test"
  chown "$marker_uid:$marker_gid" "$data_root/fifo-test"
  expect_rejected data-fifo purge purge-path-not-safe PURGE
  assert_purge_untouched
  rm -f -- "$data_root/fifo-test"

  touch "$data_root/foreign-owner-test"
  chown 65534:65534 "$data_root/foreign-owner-test"
  expect_rejected data-owner purge purge-path-not-safe PURGE
  assert_purge_untouched
  rm -f -- "$data_root/foreign-owner-test"

  install -d -o root -g root -m 0755 "$external_root"
  printf 'external-recording-sentinel\n' > "$external_root/keep.ts"
  ln -s "$recording_root" "$external_alias"
  write_recording_root "$external_alias"
  expect_rejected external-symlink purge purge-recording-root-not-safe PURGE
  assert_purge_untouched
  rm -f -- "$external_alias"

  install -d -o root -g root -m 0755 "$external_mount"
  external_mount_active=1
  mount --bind "$data_root" "$external_mount"
  write_recording_root "$external_mount"
  expect_rejected external-mount purge purge-recording-root-not-safe PURGE
  assert_purge_untouched
  umount "$external_mount"
  external_mount_active=0
  rmdir "$external_mount"
  write_recording_root "$recording_root"

  data_mount_active=1
  mount --bind "$data_root" "$data_root"
  expect_rejected target-root-mount purge purge-path-not-safe PURGE
  assert_runtime_present
  umount "$data_root"
  data_mount_active=0
  assert_purge_untouched

  data_root_swapped=1
  mv "$data_root" "$saved_data_root"
  ln -s "$saved_data_root" "$data_root"
  expect_rejected target-root-symlink purge purge-path-not-safe PURGE
  assert_runtime_present
  restore_data_root
  assert_purge_untouched

  opt_metadata_changed=1
  chmod g+w /opt
  expect_rejected writable-ancestor purge unsafe-standard-path PURGE
  assert_purge_untouched
  chmod "$opt_mode" /opt
  opt_metadata_changed=0

  opt_metadata_changed=1
  chown "$marker_uid:$opt_gid" /opt
  expect_rejected foreign-ancestor-owner purge unsafe-standard-path PURGE
  assert_purge_untouched
  chown "$opt_uid:$opt_gid" /opt
  chmod "$opt_mode" /opt
  opt_metadata_changed=0

  userdel "$account_name"
  if getent group "$account_name" >/dev/null 2>&1; then
    groupdel "$account_name"
  fi
  useradd --system --user-group --no-create-home --comment recreated-without-token \
    --home-dir "$data_root" --shell /usr/sbin/nologin "$account_name"
  expect_rejected recreated-account install account-not-managed
  assert_runtime_present
  userdel "$account_name"
  if getent group "$account_name" >/dev/null 2>&1; then
    groupdel "$account_name"
  fi
  groupadd --system --gid "$marker_gid" "$account_name"
  useradd --system --uid "$marker_uid" --gid "$account_name" --no-create-home \
    --comment "sazanami-dvr-installer-$marker_id" --home-dir "$data_root" \
    --shell /usr/sbin/nologin "$account_name"
  assert_account_present

  purge_fifo=$work_root/purge-active-input
  purge_output=$work_root/purge-active-second-preflight.out
  mkfifo "$purge_fifo"
  "$installer" purge < "$purge_fifo" > "$purge_output" 2>&1 &
  purge_pid=$!
  exec 3> "$purge_fifo"
  wait_for_prompt "$purge_output" "$purge_pid"
  install -d -o root -g root -m 0755 "$test_drop_in_root"
  printf '[Service]\nExecStart=\nExecStart=/usr/bin/sleep 300\n' > "$test_drop_in"
  chmod 0644 "$test_drop_in"
  systemctl daemon-reload
  systemctl start "$service_name"
  timeout 10 sh -c 'until systemctl is-active --quiet sazanami-dvr.service; do sleep 0.1; done'
  printf 'PURGE\n' >&3
  exec 3>&-
  if wait "$purge_pid"; then
    fail second-preflight-accepted-active-service
  fi
  purge_pid=
  grep -F service-active "$purge_output" >/dev/null || fail second-preflight-active-reason
  assert_purge_untouched
  [ -L "$wants_link" ] || fail second-preflight-active-disabled-service
  systemctl stop "$service_name"
  rm -f -- "$test_drop_in" "$purge_fifo"
  rmdir "$test_drop_in_root"
  systemctl daemon-reload

  purge_fifo=$work_root/purge-path-input
  purge_output=$work_root/purge-path-second-preflight.out
  mkfifo "$purge_fifo"
  "$installer" purge < "$purge_fifo" > "$purge_output" 2>&1 &
  purge_pid=$!
  exec 3> "$purge_fifo"
  wait_for_prompt "$purge_output" "$purge_pid"
  usr_local_bin_metadata_changed=1
  chmod g+w /usr/local/bin
  printf 'PURGE\n' >&3
  exec 3>&-
  if wait "$purge_pid"; then
    fail second-preflight-accepted-unsafe-link-ancestor
  fi
  purge_pid=
  grep -F unsafe-standard-path "$purge_output" >/dev/null || fail second-preflight-link-ancestor-reason
  assert_purge_untouched
  [ -L "$wants_link" ] || fail second-preflight-link-ancestor-disabled-service
  chmod "$usr_local_bin_mode" /usr/local/bin
  chown "$usr_local_bin_uid:$usr_local_bin_gid" /usr/local/bin
  usr_local_bin_metadata_changed=0
  rm -f -- "$purge_fifo"

  purge_fifo=$work_root/purge-runtime-input
  purge_output=$work_root/purge-runtime-second-preflight.out
  mkfifo "$purge_fifo"
  "$installer" purge < "$purge_fifo" > "$purge_output" 2>&1 &
  purge_pid=$!
  exec 3> "$purge_fifo"
  wait_for_prompt "$purge_output" "$purge_pid"
  touch "$release_root/changed-after-confirmation"
  printf 'PURGE\n' >&3
  exec 3>&-
  if wait "$purge_pid"; then
    fail second-preflight-accepted-change
  fi
  purge_pid=
  grep -F runtime-not-managed "$purge_output" >/dev/null || fail second-preflight-reason
  assert_purge_untouched
  [ -L "$wants_link" ] || fail second-preflight-disabled-service
  rm -f -- "$release_root/changed-after-confirmation" "$purge_fifo"

  write_recording_root "$external_root"
  printf 'PURGE\n' | "$installer" purge
  for removed_resource in "$install_root" "$binary_link" "$unit_link" "$wants_link" "$config_root" "$data_root"; do
    require_absent "$removed_resource"
  done
  getent passwd "$account_name" >/dev/null 2>&1 && fail purge-account-remains
  getent group "$account_name" >/dev/null 2>&1 && fail purge-group-remains
  [ -d "$external_root" ] && [ ! -L "$external_root" ] || fail external-recording-root-removed
  grep -qx external-recording-sentinel "$external_root/keep.ts" || fail external-recording-content-changed

  rm -rf -- "$external_root"
  owned_resources=0
  rm -rf -- "$work_root"
  work_root=
  trap - EXIT HUP INT TERM
  printf 'Installer lifecycle確認を完了しました: version=%s\n' "$candidate_version"
}

if [ "${0##*/}" = installer_verify.sh ]; then
  main "$@"
fi
