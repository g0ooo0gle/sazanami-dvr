#!/bin/sh
set -eu

service_name=sazanami-dvr.service
account_name=sazanami-dvr
install_root=/opt/sazanami-dvr
binary_link=/usr/local/bin/sazanami-dvr
unit_link=/etc/systemd/system/sazanami-dvr.service
wants_root=/etc/systemd/system/multi-user.target.wants
wants_link=$wants_root/sazanami-dvr.service
config_root=/etc/sazanami-dvr
data_root=/var/lib/sazanami-dvr
default_recording_root=/var/lib/sazanami-dvr/recordings
account_marker=$config_root/.installer-managed-account
runtime_marker=$install_root/.installer-managed-runtime
version_probe_user=nobody
version_probe_root=
version_probe_binary=

usage() {
  printf 'usage: %s {install|uninstall|purge}\n' "${0##*/}" >&2
}

fail() {
  printf 'Sazanami DVR installer: %s\n' "$1" >&2
  exit 1
}

fail_purge_path() {
  printf 'Sazanami DVR installer: %s\n' "$1" >&2
  printf '自動purgeは行いません。docs/linux-installation.mdの手動purge手順を確認してください。\n' >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required-command-missing:$1"
}

path_exists() {
  [ -e "$1" ] || [ -L "$1" ]
}

valid_literal_path() {
  valid_path_value=${1:-}
  case "$valid_path_value" in
    '' | / | [!/]*) return 1 ;;
    *[[:space:]]* | *"'"* | *'"'* | *'\'* | *'$'* | *'`'* | *'*'* | *'?'* | *'['* | *']'*) return 1 ;;
    */ | *//* | */./* | */../* | */. | */..) return 1 ;;
  esac
  return 0
}

valid_version_output() {
  version_output=${1:-}
  case "$version_output" in
    *'
'*) return 1 ;;
  esac
  printf '%s\n' "$version_output" |
    grep -Eq '^sazanami-dvr (0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || return 1
  version_value=${version_output#sazanami-dvr }
}

path_is_same_or_below() {
  path_base=$1
  path_candidate=$2
  case "$path_candidate" in
    "$path_base" | "$path_base"/*) return 0 ;;
    *) return 1 ;;
  esac
}

single_line_value() {
  single_line_input=${1:-}
  [ -n "$single_line_input" ] || return 1
  case "$single_line_input" in
    *'
'*) return 1 ;;
  esac
}

lookup_presence() {
  lookup_database=$1
  lookup_key=$2
  getent "$lookup_database" >/dev/null 2>&1 || return 1
  if getent "$lookup_database" "$lookup_key" >/dev/null 2>&1; then
    printf 'present\n'
  else
    lookup_status=$?
    [ "$lookup_status" -eq 2 ] || return 1
    printf 'absent\n'
  fi
}

read_purge_env() {
  purge_env_file=$1
  purge_data_root=
  purge_recording_root=
  purge_data_count=0
  purge_recording_count=0

  while IFS= read -r purge_env_line || [ -n "$purge_env_line" ]; do
    case "$purge_env_line" in
      SAZANAMI_DATA_ROOT=*)
        purge_data_count=$((purge_data_count + 1))
        purge_data_root=${purge_env_line#SAZANAMI_DATA_ROOT=}
        ;;
      SAZANAMI_RECORDING_ROOT=*)
        purge_recording_count=$((purge_recording_count + 1))
        purge_recording_root=${purge_env_line#SAZANAMI_RECORDING_ROOT=}
        ;;
    esac
  done < "$purge_env_file" || return 1

  [ "$purge_data_count" -eq 1 ] || return 1
  [ "$purge_recording_count" -eq 1 ] || return 1
  valid_literal_path "$purge_data_root" || return 1
  valid_literal_path "$purge_recording_root" || return 1
}

read_account_marker() {
  account_marker_file=$1
  marker_format=
  marker_installation_id=
  marker_uid=
  marker_gid=
  marker_format_count=0
  marker_id_count=0
  marker_uid_count=0
  marker_gid_count=0

  while IFS= read -r marker_line || [ -n "$marker_line" ]; do
    case "$marker_line" in
      format=*)
        marker_format_count=$((marker_format_count + 1))
        marker_format=${marker_line#format=}
        ;;
      installation_id=*)
        marker_id_count=$((marker_id_count + 1))
        marker_installation_id=${marker_line#installation_id=}
        ;;
      uid=*)
        marker_uid_count=$((marker_uid_count + 1))
        marker_uid=${marker_line#uid=}
        ;;
      gid=*)
        marker_gid_count=$((marker_gid_count + 1))
        marker_gid=${marker_line#gid=}
        ;;
      *) return 1 ;;
    esac
  done < "$account_marker_file" || return 1

  [ "$marker_format_count" -eq 1 ] && [ "$marker_format" = 1 ] || return 1
  [ "$marker_id_count" -eq 1 ] || return 1
  printf '%s\n' "$marker_installation_id" | grep -Eq '^[0-9a-f]{32,}$' || return 1
  [ "$marker_uid_count" -eq 1 ] && [ "$marker_gid_count" -eq 1 ] || return 1
  case "$marker_uid:$marker_gid" in
    *[!0-9:]* | :* | *:) return 1 ;;
  esac
}

read_runtime_marker() {
  runtime_marker_file=$1
  runtime_format=
  runtime_identifier=
  runtime_version=
  runtime_format_count=0
  runtime_identifier_count=0
  runtime_version_count=0

  while IFS= read -r runtime_line || [ -n "$runtime_line" ]; do
    case "$runtime_line" in
      format=*)
        runtime_format_count=$((runtime_format_count + 1))
        runtime_format=${runtime_line#format=}
        ;;
      identifier=*)
        runtime_identifier_count=$((runtime_identifier_count + 1))
        runtime_identifier=${runtime_line#identifier=}
        ;;
      version=*)
        runtime_version_count=$((runtime_version_count + 1))
        runtime_version=${runtime_line#version=}
        ;;
      *) return 1 ;;
    esac
  done < "$runtime_marker_file" || return 1

  [ "$runtime_format_count" -eq 1 ] && [ "$runtime_format" = 1 ] || return 1
  [ "$runtime_identifier_count" -eq 1 ] && [ "$runtime_identifier" = sazanami-dvr-installer ] || return 1
  [ "$runtime_version_count" -eq 1 ] || return 1
  printf '%s\n' "$runtime_version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || return 1
}

require_regular_metadata() {
  metadata_path=$1
  metadata_uid=$2
  metadata_gid=$3
  metadata_mode=$4
  [ -f "$metadata_path" ] && [ ! -L "$metadata_path" ] || return 1
  [ "$(stat -c %u "$metadata_path")" = "$metadata_uid" ] || return 1
  [ "$(stat -c %g "$metadata_path")" = "$metadata_gid" ] || return 1
  [ "$(stat -c %a "$metadata_path")" = "$metadata_mode" ] || return 1
  [ "$(stat -c %h "$metadata_path")" = 1 ] || return 1
}

require_directory_metadata() {
  metadata_path=$1
  metadata_uid=$2
  metadata_gid=$3
  metadata_mode=$4
  [ -d "$metadata_path" ] && [ ! -L "$metadata_path" ] || return 1
  [ "$(stat -c %u "$metadata_path")" = "$metadata_uid" ] || return 1
  [ "$(stat -c %g "$metadata_path")" = "$metadata_gid" ] || return 1
  [ "$(stat -c %a "$metadata_path")" = "$metadata_mode" ] || return 1
}

root_controlled_directory() {
  controlled_path=$1
  [ -d "$controlled_path" ] && [ ! -L "$controlled_path" ] || return 1
  [ "$(stat -c %u "$controlled_path")" -eq 0 ] || return 1
  controlled_mode=$(stat -c %a "$controlled_path") || return 1
  case "$controlled_mode" in
    *[!0-7]* | '') return 1 ;;
  esac
  [ $((0$controlled_mode & 0022)) -eq 0 ] || return 1
}

mount_at_or_below() {
  mount_root=$1
  awk -v root="$mount_root" '
    $5 == root || index($5, root "/") == 1 { found = 1 }
    END { exit found ? 0 : 1 }
  ' /proc/self/mountinfo
}

mount_is_exact() {
  mount_path=$1
  awk -v path="$mount_path" '
    $5 == path { found = 1 }
    END { exit found ? 0 : 1 }
  ' /proc/self/mountinfo
}

service_inactive_preflight() {
  service_active_state=$(systemctl show "$service_name" --property=ActiveState --value 2>/dev/null) ||
    fail systemd-required
  case "$service_active_state" in
    active | activating | reloading | deactivating) fail service-active ;;
    inactive | failed) ;;
    *) fail systemd-required ;;
  esac
}

no_mount_at_or_below() {
  if mount_at_or_below "$1"; then
    return 1
  else
    mount_query_status=$?
  fi
  [ "$mount_query_status" -eq 1 ]
}

not_mountpoint() {
  if mount_is_exact "$1"; then
    return 1
  else
    mount_query_status=$?
  fi
  [ "$mount_query_status" -eq 1 ]
}

required_commands() {
  for command_name in awk chmod chown dirname find getent grep groupdel id install ln mktemp mv od pgrep readlink rm rmdir runuser sed stat systemctl timeout tr uname useradd userdel; do
    require_command "$command_name"
  done
}

base_preflight() {
  [ "$(id -u)" -eq 0 ] || fail root-required
  [ "$(uname -s)" = Linux ] || fail linux-required
  required_commands
  [ -d /run/systemd/system ] || fail systemd-required
  [ -r /proc/self/mountinfo ] || fail mount-state-unavailable
  systemctl --version >/dev/null 2>&1 || fail systemd-required

  for controlled_path in /opt /etc /var /var/lib /usr /usr/local /usr/local/bin /etc/systemd /etc/systemd/system; do
    root_controlled_directory "$controlled_path" || fail unsafe-standard-path
  done

  service_inactive_preflight
}

regular_source_file() {
  source_path=$1
  [ -f "$source_path" ] && [ ! -L "$source_path" ] || return 1
  [ "$(stat -c %h "$source_path")" = 1 ] || return 1
}

regular_source_directory() {
  source_directory=$1
  [ -d "$source_directory" ] && [ ! -L "$source_directory" ]
}

cleanup_version_probe() {
  case "${version_probe_root:-}" in
    /run/sazanami-dvr-version-probe.[A-Za-z0-9]*)
      if [ -d "$version_probe_root" ] && [ ! -L "$version_probe_root" ]; then
        if [ -n "$version_probe_binary" ]; then
          rm -f -- "$version_probe_binary"
        fi
        rmdir "$version_probe_root" 2>/dev/null || true
      fi
      ;;
  esac
  version_probe_root=
  version_probe_binary=
}

probe_source_version() {
  version_probe_source=$1
  version_probe_entry=$(getent passwd "$version_probe_user") || fail version-probe-unavailable
  single_line_value "$version_probe_entry" || fail version-probe-unavailable
  IFS=: read -r version_probe_name version_probe_password version_probe_uid version_probe_gid \
    version_probe_comment version_probe_home version_probe_shell <<EOF
$version_probe_entry
EOF
  [ "$version_probe_name" = "$version_probe_user" ] || fail version-probe-unavailable
  case "$version_probe_uid:$version_probe_gid" in
    *[!0-9:]* | :* | *:) fail version-probe-unavailable ;;
  esac
  [ "$version_probe_uid" -ne 0 ] && [ "$version_probe_gid" -ne 0 ] || fail version-probe-unavailable
  version_probe_groups=$(id -G "$version_probe_user") || fail version-probe-unavailable
  for version_probe_group in $version_probe_groups; do
    [ "$version_probe_group" -ne 0 ] || fail version-probe-unavailable
  done

  root_controlled_directory /run || fail version-probe-unavailable
  version_probe_root=$(mktemp -d /run/sazanami-dvr-version-probe.XXXXXX) || fail version-probe-unavailable
  case "$version_probe_root" in
    /run/sazanami-dvr-version-probe.[A-Za-z0-9]*) ;;
    *) fail version-probe-unavailable ;;
  esac
  trap cleanup_version_probe EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  chmod 0755 "$version_probe_root"
  version_probe_binary=$version_probe_root/sazanami-dvr
  install -o root -g root -m 0755 "$version_probe_source" "$version_probe_binary" || fail version-invalid

  version_probe_status=0
  source_version_output=$(timeout --kill-after=2 5 runuser -u "$version_probe_user" -- "$version_probe_binary" --version 2>/dev/null) ||
    version_probe_status=$?
  cleanup_version_probe
  trap - EXIT HUP INT TERM
  [ "$version_probe_status" -eq 0 ] || fail version-invalid
  valid_version_output "$source_version_output" || fail version-invalid
  source_version=${source_version_output#sazanami-dvr }
}

load_source_release() {
  invoked_script=$0
  case "$invoked_script" in
    /*) ;;
    *) invoked_script=$PWD/$invoked_script ;;
  esac
  [ ! -L "$invoked_script" ] && [ -f "$invoked_script" ] || fail source-invalid
  source_script_dir_logical=$(CDPATH= cd -- "$(dirname -- "$invoked_script")" && pwd -L) || fail source-invalid
  source_script_dir=$(CDPATH= cd -- "$(dirname -- "$invoked_script")" && pwd -P) || fail source-invalid
  [ "$source_script_dir_logical" = "$source_script_dir" ] || fail source-invalid
  archive_root=$(CDPATH= cd -- "$source_script_dir/.." && pwd -P) || fail source-invalid
  [ "$source_script_dir" = "$archive_root/packaging" ] || fail source-invalid
  for source_directory in "$archive_root" "$archive_root/docs" "$archive_root/packaging" \
    "$archive_root/packaging/systemd"; do
    regular_source_directory "$source_directory" || fail source-invalid
  done

  for source_relative in \
    sazanami-dvr \
    LICENSE \
    README.md \
    CHANGELOG.md \
    THIRD_PARTY_NOTICES.md \
    docs/linux-installation.md \
    docs/docker-compose.md \
    packaging/install.sh \
    packaging/systemd/sazanami-dvr.service \
    packaging/systemd/sazanami-dvr.env.example; do
    regular_source_file "$archive_root/$source_relative" || fail source-invalid
  done
  [ -x "$archive_root/sazanami-dvr" ] || fail source-invalid
  [ -x "$archive_root/packaging/install.sh" ] || fail source-invalid

  probe_source_version "$archive_root/sazanami-dvr"
}

write_account_marker() {
  write_marker_id=$1
  write_marker_uid=$2
  write_marker_gid=$3
  account_marker_temp=$config_root/.installer-managed-account.tmp.$$
  path_exists "$account_marker_temp" && fail existing-resource
  created_account_marker_temp=1
  umask 077
  {
    printf 'format=1\n'
    printf 'installation_id=%s\n' "$write_marker_id"
    printf 'uid=%s\n' "$write_marker_uid"
    printf 'gid=%s\n' "$write_marker_gid"
  } > "$account_marker_temp"
  chown root:root "$account_marker_temp"
  chmod 0600 "$account_marker_temp"
  mv "$account_marker_temp" "$account_marker"
  created_account_marker_temp=0
}

write_runtime_marker() {
  write_runtime_version=$1
  runtime_marker_temp=$install_root/.installer-managed-runtime.tmp.$$
  path_exists "$runtime_marker_temp" && fail existing-resource
  created_runtime_marker_temp=1
  umask 077
  {
    printf 'format=1\n'
    printf 'identifier=sazanami-dvr-installer\n'
    printf 'version=%s\n' "$write_runtime_version"
  } > "$runtime_marker_temp"
  chown root:root "$runtime_marker_temp"
  chmod 0600 "$runtime_marker_temp"
  mv "$runtime_marker_temp" "$runtime_marker"
  created_runtime_marker_temp=0
}

load_current_account() {
  require_regular_metadata "$account_marker" 0 0 600 || return 1
  read_account_marker "$account_marker" || return 1

  account_entry=$(getent passwd "$account_name") || return 1
  single_line_value "$account_entry" || return 1
  IFS=: read -r account_user account_password account_uid account_gid account_comment account_home account_shell <<EOF
$account_entry
EOF
  [ "$account_user" = "$account_name" ] || return 1
  [ "$account_uid" = "$marker_uid" ] && [ "$account_gid" = "$marker_gid" ] || return 1
  [ "$account_comment" = "sazanami-dvr-installer-$marker_installation_id" ] || return 1
  [ "$account_home" = "$data_root" ] || return 1
  [ "$account_shell" = /usr/sbin/nologin ] || return 1

  group_entry=$(getent group "$account_name") || return 1
  single_line_value "$group_entry" || return 1
  IFS=: read -r account_group account_group_password account_group_gid account_group_members <<EOF
$group_entry
EOF
  [ "$account_group" = "$account_name" ] || return 1
  [ "$account_group_gid" = "$account_gid" ] || return 1
  [ -z "$account_group_members" ] || return 1
  passwd_entries=$(getent passwd) || return 1
  if printf '%s\n' "$passwd_entries" | awk -F: -v gid="$account_gid" -v user="$account_name" \
    '$4 == gid && $1 != user { found = 1 } END { exit found ? 0 : 1 }'; then
    return 1
  fi
  return 0
}

account_matches_created_values() {
  created_check_id=$1
  created_check_uid=$2
  created_check_gid=$3
  account_entry=$(getent passwd "$account_name") || return 1
  single_line_value "$account_entry" || return 1
  IFS=: read -r account_user account_password account_uid account_gid account_comment account_home account_shell <<EOF
$account_entry
EOF
  [ "$account_user" = "$account_name" ] || return 1
  [ "$account_uid" = "$created_check_uid" ] && [ "$account_gid" = "$created_check_gid" ] || return 1
  [ "$account_comment" = "sazanami-dvr-installer-$created_check_id" ] || return 1
  [ "$account_home" = "$data_root" ] && [ "$account_shell" = /usr/sbin/nologin ] || return 1
  group_entry=$(getent group "$account_name") || return 1
  single_line_value "$group_entry" || return 1
  IFS=: read -r account_group account_group_password account_group_gid account_group_members <<EOF
$group_entry
EOF
  [ "$account_group" = "$account_name" ] || return 1
  [ "$account_group_gid" = "$created_check_gid" ] && [ -z "$account_group_members" ] || return 1
  passwd_entries=$(getent passwd) || return 1
  if printf '%s\n' "$passwd_entries" | awk -F: -v gid="$created_check_gid" -v user="$account_name" \
    '$4 == gid && $1 != user { found = 1 } END { exit found ? 0 : 1 }'; then
    return 1
  fi
  account_has_processes && return 1
  return 0
}

account_has_processes() {
  if pgrep -u "$account_uid" >/dev/null 2>&1; then
    return 0
  else
    process_status=$?
  fi
  [ "$process_status" -eq 1 ] || return 0
  return 1
}

runtime_release_root() {
  printf '%s/%s\n' "$install_root" "$runtime_version"
}

require_runtime_file() {
  require_regular_metadata "$1" 0 0 "$2" || return 1
}

validate_runtime_inventory() {
  require_regular_metadata "$runtime_marker" 0 0 600 || return 1
  read_runtime_marker "$runtime_marker" || return 1
  release_root=$(runtime_release_root)

  require_directory_metadata "$install_root" 0 0 755 || return 1
  require_directory_metadata "$release_root" 0 0 755 || return 1
  require_directory_metadata "$release_root/docs" 0 0 755 || return 1
  require_directory_metadata "$release_root/packaging" 0 0 755 || return 1
  require_directory_metadata "$release_root/packaging/systemd" 0 0 755 || return 1
  require_runtime_file "$release_root/sazanami-dvr" 755 || return 1
  require_runtime_file "$release_root/packaging/install.sh" 755 || return 1
  for runtime_document in LICENSE README.md CHANGELOG.md THIRD_PARTY_NOTICES.md docs/linux-installation.md docs/docker-compose.md packaging/systemd/sazanami-dvr.service packaging/systemd/sazanami-dvr.env.example; do
    require_runtime_file "$release_root/$runtime_document" 644 || return 1
  done

  no_mount_at_or_below "$install_root" || return 1

  runtime_inventory=$(find "$install_root" -mindepth 1 -print) || return 1
  while IFS= read -r inventory_path; do
    case "$inventory_path" in
      "$runtime_marker"|"$release_root"|"$release_root/sazanami-dvr"|\
      "$release_root/LICENSE"|"$release_root/README.md"|\
      "$release_root/CHANGELOG.md"|"$release_root/THIRD_PARTY_NOTICES.md"|\
      "$release_root/docs"|"$release_root/docs/linux-installation.md"|\
      "$release_root/docs/docker-compose.md"|"$release_root/packaging"|\
      "$release_root/packaging/install.sh"|"$release_root/packaging/systemd"|\
      "$release_root/packaging/systemd/sazanami-dvr.service"|\
      "$release_root/packaging/systemd/sazanami-dvr.env.example") ;;
      *) return 1 ;;
    esac
  done <<EOF
$runtime_inventory
EOF
}

links_match_runtime() {
  release_root=$(runtime_release_root)
  [ -L "$binary_link" ] && [ "$(readlink "$binary_link")" = "$release_root/sazanami-dvr" ] || return 1
  [ -L "$unit_link" ] && [ "$(readlink "$unit_link")" = "$release_root/packaging/systemd/sazanami-dvr.service" ] || return 1
  if path_exists "$wants_link"; then
    [ -L "$wants_link" ] || return 1
    [ "$(readlink -f -- "$wants_link")" = "$release_root/packaging/systemd/sazanami-dvr.service" ] || return 1
  fi
}

remove_runtime_files() {
  remove_version=$1
  remove_root=$install_root/$remove_version
  rm -f -- "$remove_root/docs/linux-installation.md" "$remove_root/docs/docker-compose.md"
  rmdir "$remove_root/docs" 2>/dev/null || true
  rm -f -- "$remove_root/packaging/systemd/sazanami-dvr.service" "$remove_root/packaging/systemd/sazanami-dvr.env.example"
  rmdir "$remove_root/packaging/systemd" 2>/dev/null || true
  rm -f -- "$remove_root/packaging/install.sh"
  rmdir "$remove_root/packaging" 2>/dev/null || true
  rm -f -- "$remove_root/sazanami-dvr" "$remove_root/LICENSE" "$remove_root/README.md" \
    "$remove_root/CHANGELOG.md" "$remove_root/THIRD_PARTY_NOTICES.md"
  rmdir "$remove_root" 2>/dev/null || true
  rm -f -- "$runtime_marker"
  rmdir "$install_root" 2>/dev/null || true
}

install_mode=
runtime_state=

fresh_unit_conflict_preflight() {
  for conflicting_unit_path in \
    "/run/systemd/system/$service_name" \
    "/run/systemd/transient/$service_name" \
    "/usr/local/lib/systemd/system/$service_name" \
    "/usr/lib/systemd/system/$service_name" \
    "/lib/systemd/system/$service_name" \
    "/etc/systemd/system/$service_name.d" \
    "/run/systemd/system/$service_name.d" \
    "/usr/local/lib/systemd/system/$service_name.d" \
    "/usr/lib/systemd/system/$service_name.d" \
    "/lib/systemd/system/$service_name.d" \
    "/etc/systemd/system/multi-user.target.wants/$service_name"; do
    path_exists "$conflicting_unit_path" && fail existing-resource
  done
}

install_preflight() {
  load_source_release
  account_lookup=$(lookup_presence passwd "$account_name") || fail account-lookup-failed
  group_lookup=$(lookup_presence group "$account_name") || fail account-lookup-failed

  if [ "$account_lookup" = absent ] && [ "$group_lookup" = absent ]; then
    for fresh_path in "$install_root" "$binary_link" "$unit_link" "$config_root" "$data_root"; do
      path_exists "$fresh_path" && fail existing-resource
    done
    fresh_unit_conflict_preflight
    install_mode=fresh
  elif [ "$account_lookup" = present ] && [ "$group_lookup" = present ]; then
    load_current_account || fail account-not-managed
    require_directory_metadata "$config_root" 0 "$account_gid" 750 || fail existing-resource
    require_directory_metadata "$data_root" "$account_uid" "$account_gid" 700 || fail existing-resource
    require_directory_metadata "$default_recording_root" "$account_uid" "$account_gid" 700 || fail existing-resource
    require_regular_metadata "$config_root/sazanami-dvr.env" 0 0 600 || fail existing-resource
    install_mode=managed
  else
    fail account-not-managed
  fi

  if ! path_exists "$install_root" && ! path_exists "$binary_link" && ! path_exists "$unit_link"; then
    runtime_state=absent
  elif path_exists "$install_root" && path_exists "$binary_link" && path_exists "$unit_link"; then
    validate_runtime_inventory || fail runtime-not-managed
    [ "$runtime_version" = "$source_version" ] || fail manual-update-required
    links_match_runtime || fail link-not-managed
    runtime_state=present
  else
    fail runtime-not-managed
  fi
}

created_account=0
created_uid=
created_gid=
created_installation_id=
created_config_root=0
created_data_root=0
created_recording_root=0
created_env=0
created_account_marker=0
created_runtime=0
created_binary_link=0
created_unit_link=0
created_account_marker_temp=0
created_runtime_marker_temp=0
install_completed=0

cleanup_failed_install() {
  cleanup_result=$?
  trap - EXIT HUP INT TERM
  if [ "$install_completed" -ne 1 ]; then
    cleanup_account_safe=0
    if [ "$created_account" -eq 1 ] && [ -n "$created_uid" ] && [ -n "$created_gid" ] &&
      account_matches_created_values "$created_installation_id" "$created_uid" "$created_gid"; then
      cleanup_account_safe=1
    fi
    if [ "$created_unit_link" -eq 1 ] && [ -L "$unit_link" ]; then
      rm -f -- "$unit_link"
    fi
    if [ "$created_binary_link" -eq 1 ] && [ -L "$binary_link" ]; then
      rm -f -- "$binary_link"
    fi
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [ "$created_runtime_marker_temp" -eq 1 ] &&
      require_regular_metadata "$runtime_marker_temp" 0 0 600; then
      rm -f -- "$runtime_marker_temp"
    fi
    if [ "$created_runtime" -eq 1 ]; then
      remove_runtime_files "$source_version"
    fi
    if [ "$created_env" -eq 1 ] && require_regular_metadata "$config_root/sazanami-dvr.env" 0 0 600; then
      rm -f -- "$config_root/sazanami-dvr.env"
    fi
    if [ "$created_account_marker" -eq 1 ] && [ "$cleanup_account_safe" -eq 1 ] &&
      require_regular_metadata "$account_marker" 0 0 600; then
      rm -f -- "$account_marker"
    fi
    if [ "$created_account_marker_temp" -eq 1 ] &&
      require_regular_metadata "$account_marker_temp" 0 0 600; then
      rm -f -- "$account_marker_temp"
    fi
    [ "$created_recording_root" -eq 1 ] && rmdir "$default_recording_root" 2>/dev/null || true
    [ "$created_data_root" -eq 1 ] && rmdir "$data_root" 2>/dev/null || true
    [ "$created_config_root" -eq 1 ] && rmdir "$config_root" 2>/dev/null || true
    if [ "$cleanup_account_safe" -eq 1 ]; then
      userdel "$account_name" >/dev/null 2>&1 || true
      if getent group "$account_name" >/dev/null 2>&1; then
        groupdel "$account_name" >/dev/null 2>&1 || true
      fi
    elif [ "$created_account" -eq 1 ]; then
      printf 'Sazanami DVR installer: rollback-account-not-safe\n' >&2
    fi
  fi
  exit "$cleanup_result"
}

perform_install() {
  install_preflight
  if [ "$runtime_state" = present ]; then
    printf 'Sazanami DVR %sはinstaller管理下で導入済みです。\n' "$source_version"
    return
  fi

  trap cleanup_failed_install EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  if [ "$install_mode" = fresh ]; then
    created_installation_id=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    printf '%s\n' "$created_installation_id" | grep -Eq '^[0-9a-f]{32}$' || fail random-id-failed
    useradd --system --user-group --no-create-home \
      --comment "sazanami-dvr-installer-$created_installation_id" \
      --home-dir "$data_root" --shell /usr/sbin/nologin "$account_name"
    created_account=1
    created_uid=$(id -u "$account_name")
    created_gid=$(id -g "$account_name")

    created_config_root=1
    install -d -o root -g "$created_gid" -m 0750 "$config_root"
    created_data_root=1
    install -d -o "$created_uid" -g "$created_gid" -m 0700 "$data_root"
    created_recording_root=1
    install -d -o "$created_uid" -g "$created_gid" -m 0700 "$default_recording_root"
    created_env=1
    install -o root -g root -m 0600 "$archive_root/packaging/systemd/sazanami-dvr.env.example" \
      "$config_root/sazanami-dvr.env"
    write_account_marker "$created_installation_id" "$created_uid" "$created_gid"
    created_account_marker=1
  fi

  release_root=$install_root/$source_version
  created_runtime=1
  install -d -o root -g root -m 0755 "$install_root" "$release_root" "$release_root/docs" \
    "$release_root/packaging" "$release_root/packaging/systemd"
  install -o root -g root -m 0755 "$archive_root/sazanami-dvr" "$release_root/sazanami-dvr"
  install -o root -g root -m 0755 "$archive_root/packaging/install.sh" "$release_root/packaging/install.sh"
  for runtime_document in LICENSE README.md CHANGELOG.md THIRD_PARTY_NOTICES.md; do
    install -o root -g root -m 0644 "$archive_root/$runtime_document" "$release_root/$runtime_document"
  done
  install -o root -g root -m 0644 "$archive_root/docs/linux-installation.md" "$release_root/docs/linux-installation.md"
  install -o root -g root -m 0644 "$archive_root/docs/docker-compose.md" "$release_root/docs/docker-compose.md"
  install -o root -g root -m 0644 "$archive_root/packaging/systemd/sazanami-dvr.service" \
    "$release_root/packaging/systemd/sazanami-dvr.service"
  install -o root -g root -m 0644 "$archive_root/packaging/systemd/sazanami-dvr.env.example" \
    "$release_root/packaging/systemd/sazanami-dvr.env.example"
  write_runtime_marker "$source_version"

  created_binary_link=1
  ln -s "$release_root/sazanami-dvr" "$binary_link"
  created_unit_link=1
  ln -s "$release_root/packaging/systemd/sazanami-dvr.service" "$unit_link"
  systemctl daemon-reload

  validate_runtime_inventory || fail runtime-not-managed
  links_match_runtime || fail link-not-managed
  install_completed=1
  trap - EXIT HUP INT TERM

  printf 'Sazanami DVR %sを配置しました。DBとチャンネル設定を確認してからサービスを有効化してください。\n' "$source_version"
  printf '次の手順: %s/docs/linux-installation.md\n' "$release_root"
}

uninstall_preflight() {
  validate_runtime_inventory || fail runtime-not-managed
  links_match_runtime || fail link-not-managed
}

perform_uninstall_prechecked() {
  uninstall_version=$runtime_version
  if path_exists "$wants_link"; then
    rm -f -- "$wants_link"
  fi
  rm -f -- "$unit_link" "$binary_link"
  systemctl daemon-reload
  remove_runtime_files "$uninstall_version"
}

perform_uninstall() {
  uninstall_preflight
  perform_uninstall_prechecked
  printf 'Sazanami DVRの配布物を削除しました。設定、DB、録画、backup、専用利用者は残しています。\n'
}

validate_purge_tree() {
  purge_tree_root=$1
  purge_allowed_uid=$2
  [ -d "$purge_tree_root" ] && [ ! -L "$purge_tree_root" ] || return 1
  no_mount_at_or_below "$purge_tree_root" || return 1
  purge_probe=$(find "$purge_tree_root" -xdev ! \( -type f -o -type d \) -printf x -quit) || return 1
  [ -z "$purge_probe" ] || return 1
  purge_probe=$(find "$purge_tree_root" -xdev -type f -links +1 -printf x -quit) || return 1
  [ -z "$purge_probe" ] || return 1
  purge_probe=$(find "$purge_tree_root" -xdev ! \( -uid 0 -o -uid "$purge_allowed_uid" \) -printf x -quit) || return 1
  [ -z "$purge_probe" ] || return 1
  return 0
}

validate_external_recording_root() {
  external_root=$1
  [ "$external_root" != "$default_recording_root" ] || return 0
  for deleted_root in "$install_root" "$config_root" "$data_root"; do
    path_is_same_or_below "$deleted_root" "$external_root" && return 1
  done

  component_path=
  component_rest=${external_root#/}
  while [ -n "$component_rest" ]; do
    component_name=${component_rest%%/*}
    if [ "$component_rest" = "$component_name" ]; then
      component_rest=
    else
      component_rest=${component_rest#*/}
    fi
    component_path=$component_path/$component_name
    [ -d "$component_path" ] && [ ! -L "$component_path" ] || return 1
    not_mountpoint "$component_path" || return 1
  done

  physical_root=$(readlink -f -- "$external_root") || return 1
  [ -n "$physical_root" ] || return 1
  path_is_same_or_below "$data_root" "$physical_root" && return 1
  return 0
}

purge_preflight() {
  base_preflight
  uninstall_preflight
  load_current_account || fail account-not-managed
  account_has_processes && fail account-has-processes

  for purge_ancestor in /opt /etc /var /var/lib /usr/local /usr/local/bin /etc/systemd /etc/systemd/system; do
    root_controlled_directory "$purge_ancestor" || fail_purge_path purge-path-not-safe
    not_mountpoint "$purge_ancestor" || fail_purge_path purge-path-not-safe
  done
  if path_exists "$wants_root"; then
    root_controlled_directory "$wants_root" || fail_purge_path purge-path-not-safe
    not_mountpoint "$wants_root" || fail_purge_path purge-path-not-safe
  fi
  for purge_root in "$install_root" "$config_root" "$data_root"; do
    [ -d "$purge_root" ] && [ ! -L "$purge_root" ] || fail_purge_path purge-path-not-safe
    not_mountpoint "$purge_root" || fail_purge_path purge-path-not-safe
  done

  require_regular_metadata "$config_root/sazanami-dvr.env" 0 0 600 || fail purge-env-not-safe
  read_purge_env "$config_root/sazanami-dvr.env" || fail purge-env-not-safe
  [ "$purge_data_root" = "$data_root" ] || fail purge-env-not-safe
  validate_external_recording_root "$purge_recording_root" || fail_purge_path purge-recording-root-not-safe

  validate_purge_tree "$install_root" "$account_uid" || fail_purge_path purge-path-not-safe
  validate_purge_tree "$config_root" "$account_uid" || fail_purge_path purge-path-not-safe
  validate_purge_tree "$data_root" "$account_uid" || fail_purge_path purge-path-not-safe
}

remove_purge_tree() {
  purge_remove_target=$1
  case "$purge_remove_target" in
    "$config_root" | "$data_root") ;;
    *) fail purge-path-not-safe ;;
  esac
  [ -d "$purge_remove_target" ] && [ ! -L "$purge_remove_target" ] || fail purge-path-not-safe
  no_mount_at_or_below "$purge_remove_target" || fail purge-path-not-safe
  rm -rf -- "$purge_remove_target"
}

perform_purge() {
  purge_preflight
  printf '次のinstaller管理対象を削除します:\n'
  printf '  /opt/sazanami-dvr\n  /etc/sazanami-dvr\n  /var/lib/sazanami-dvr\n  sazanami-dvr利用者とgroup\n'
  printf '標準data外の録画先は削除しません。続行するにはPURGEと入力してください: '
  if ! IFS= read -r purge_answer; then
    fail purge-confirmation-required
  fi
  [ "$purge_answer" = PURGE ] || fail purge-confirmation-required
  purge_preflight

  perform_uninstall_prechecked
  remove_purge_tree "$config_root"
  remove_purge_tree "$data_root"
  userdel "$account_name"
  if getent group "$account_name" >/dev/null 2>&1; then
    groupdel "$account_name"
  fi
  printf 'Sazanami DVRのinstaller管理対象をpurgeしました。\n'
}

installer_main() {
  [ "$#" -eq 1 ] || {
    usage
    exit 2
  }
  case "$1" in
    install | uninstall | purge) installer_operation=$1 ;;
    *)
      usage
      exit 2
      ;;
  esac

  base_preflight
  case "$installer_operation" in
    install) perform_install ;;
    uninstall) perform_uninstall ;;
    purge) perform_purge ;;
  esac
}

if [ "${0##*/}" = install.sh ]; then
  installer_main "$@"
fi
