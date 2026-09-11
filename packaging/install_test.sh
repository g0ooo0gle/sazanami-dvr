#!/bin/sh
set -eu

script_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
installer="$script_root/install.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/sazanami-install-test.XXXXXX")
test_root=$(printf '%s\n' "$test_root" | sed 's://*:/:g')

case "$test_root" in
  */sazanami-install-test.*) ;;
  *)
    printf '安全な一時directoryを作れませんでした\n' >&2
    exit 1
    ;;
esac

cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT HUP INT TERM

test_fail() {
  printf 'Installer contract testに失敗しました: %s\n' "$*" >&2
  exit 1
}

expect_rejected() {
  if ("$@") >/dev/null 2>&1; then
    test_fail "拒否される入力を受理しました: $*"
  fi
}

[ -f "$installer" ] || test_fail "installer-missing"
[ -x "$installer" ] || test_fail "installer-not-executable"
sh -n "$installer"

if grep -F 'systemctl disable "$service_name"' "$installer" >/dev/null; then
  test_fail "管理対象外のsystemd linkまで削除し得るdisableを使用しています"
fi
grep -F 'runuser -u "$version_probe_user" -- "$version_probe_binary" --version' "$installer" >/dev/null ||
  test_fail "配布binaryのversion確認が非特権実行ではありません"
if grep -F '"$archive_root/sazanami-dvr" --version' "$installer" >/dev/null; then
  test_fail "配布binaryを配布元から直接実行しています"
fi

for arguments in '' 'unknown' 'install extra' 'uninstall extra' 'purge extra'; do
  output="$test_root/usage.out"
  if [ -n "$arguments" ]; then
    # shellcheck disable=SC2086 -- split is intentional for CLI argument tests.
    if sh "$installer" $arguments >"$output" 2>&1; then
      test_fail "usage errorが成功しました: $arguments"
    fi
  elif sh "$installer" >"$output" 2>&1; then
    test_fail "引数なしが成功しました"
  fi
  grep -F 'usage:' "$output" >/dev/null || test_fail "usageを表示しませんでした: $arguments"
done

# Source only the pure contract helpers. install.sh runs main only when invoked directly.
. "$installer"

for accepted_path in /var/lib/sazanami-dvr /var/lib/sazanami-dvr/recordings /srv/recordings-01 \
  '/srv/録画+tv@example'; do
  valid_literal_path "$accepted_path" || test_fail "正常pathを拒否しました: $accepted_path"
done

for rejected_path in '' / relative /tmp/../var /tmp/./var /tmp//var /tmp/var/ '/tmp/a b' '/tmp/$value' '/tmp/*' '/tmp/a\b' '/tmp/`id`'; do
  expect_rejected valid_literal_path "$rejected_path"
done

valid_version_output 'sazanami-dvr 1.2.3' || test_fail "正常version出力を拒否しました"
expect_rejected valid_version_output 'sazanami-dvr 01.2.3'
expect_rejected valid_version_output 'sazanami-dvr 1.2.3
unexpected-output'

path_is_same_or_below /var/lib/sazanami-dvr /var/lib/sazanami-dvr
path_is_same_or_below /var/lib/sazanami-dvr /var/lib/sazanami-dvr/custom
expect_rejected path_is_same_or_below /var/lib/sazanami-dvr /var/lib/sazanami-dvr2
expect_rejected path_is_same_or_below /var/lib/sazanami-dvr /srv/recordings

safe_env="$test_root/safe.env"
not_sourced="$test_root/not-sourced"
{
  printf '%s\n' 'SAZANAMI_DATA_ROOT=/var/lib/sazanami-dvr'
  printf 'UNRELATED=$(touch %s)\n' "$not_sourced"
  printf '%s\n' 'SAZANAMI_RECORDING_ROOT=/srv/recordings'
} >"$safe_env"
read_purge_env "$safe_env"
[ "$purge_data_root" = /var/lib/sazanami-dvr ] || test_fail "data rootの解析結果が違います"
[ "$purge_recording_root" = /srv/recordings ] || test_fail "recording rootの解析結果が違います"
[ ! -e "$not_sourced" ] || test_fail "環境設定をsourceしました"

duplicate_env="$test_root/duplicate.env"
{
  printf '%s\n' 'SAZANAMI_DATA_ROOT=/var/lib/sazanami-dvr'
  printf '%s\n' 'SAZANAMI_DATA_ROOT=/var/lib/sazanami-dvr'
  printf '%s\n' 'SAZANAMI_RECORDING_ROOT=/var/lib/sazanami-dvr/recordings'
} >"$duplicate_env"
expect_rejected read_purge_env "$duplicate_env"

expanded_env="$test_root/expanded.env"
{
  printf '%s\n' 'SAZANAMI_DATA_ROOT=/var/lib/sazanami-dvr'
  printf '%s\n' 'SAZANAMI_RECORDING_ROOT=$HOME/recordings'
} >"$expanded_env"
expect_rejected read_purge_env "$expanded_env"

marker="$test_root/account.marker"
{
  printf '%s\n' 'format=1'
  printf '%s\n' 'installation_id=0123456789abcdef0123456789abcdef'
  printf '%s\n' 'uid=123'
  printf '%s\n' 'gid=456'
} >"$marker"
read_account_marker "$marker"
[ "$marker_format" = 1 ] || test_fail "marker formatが違います"
[ "$marker_installation_id" = 0123456789abcdef0123456789abcdef ] || test_fail "marker idが違います"
[ "$marker_uid" = 123 ] || test_fail "marker uidが違います"
[ "$marker_gid" = 456 ] || test_fail "marker gidが違います"

invalid_marker="$test_root/invalid.marker"
{
  printf '%s\n' 'format=1'
  printf '%s\n' 'installation_id=0123456789abcdef0123456789abcdef'
  printf '%s\n' 'installation_id=ffffffffffffffffffffffffffffffff'
  printf '%s\n' 'uid=123'
  printf '%s\n' 'gid=456'
} >"$invalid_marker"
expect_rejected read_account_marker "$invalid_marker"

expect_rejected validate_external_recording_root "$install_root/recordings"
expect_rejected validate_external_recording_root "$config_root/recordings"
expect_rejected validate_external_recording_root "$data_root/custom-recordings"

account_uid=123
pgrep() {
  return 1
}
expect_rejected account_has_processes
pgrep() {
  return 0
}
account_has_processes || test_fail "実行中processを検出しませんでした"
pgrep() {
  return 2
}
account_has_processes || test_fail "process確認errorを安全側へ倒しませんでした"

getent() {
  [ "$#" -eq 1 ] && return 0
  return 2
}
[ "$(lookup_presence passwd sazanami-dvr)" = absent ] || test_fail "不存在accountを判定できませんでした"
getent() {
  return 0
}
[ "$(lookup_presence passwd sazanami-dvr)" = present ] || test_fail "既存accountを判定できませんでした"
getent() {
  [ "$#" -eq 1 ] && return 0
  return 3
}
expect_rejected lookup_presence passwd sazanami-dvr

systemctl() {
  printf 'inactive\n'
}
service_inactive_preflight
systemctl() {
  printf 'active\n'
}
expect_rejected service_inactive_preflight
systemctl() {
  return 1
}
expect_rejected service_inactive_preflight

wants_symlink_target="$test_root/wants-symlink-target"
wants_root="$test_root/wants-symlink"
mkdir "$wants_symlink_target"
ln -s "$wants_symlink_target" "$wants_root"
expect_rejected wants_root_preflight
rm -f -- "$wants_root"

wants_root="$test_root/wants-root"
mkdir "$wants_root"
root_controlled_directory() {
  [ "$1" = "$wants_root" ]
}
not_mountpoint() {
  [ "$1" = "$wants_root" ]
}
wants_root_preflight || test_fail "安全なwants rootを受理できませんでした"
root_controlled_directory() {
  return 1
}
expect_rejected wants_root_preflight
root_controlled_directory() {
  return 0
}
not_mountpoint() {
  return 1
}
expect_rejected wants_root_preflight

systemd_search_root="$test_root/systemd-search"
mkdir "$systemd_search_root"
systemd_unit_paths() {
  printf '%s\n' "$systemd_search_root"
}
runtime_state=absent
fresh_unit_conflict_preflight

for conflict_relative in \
  "$service_name" \
  "$service_name.d" \
  "$service_name.wants" \
  "$service_name.requires" \
  "$service_name.upholds" \
  "sazanami-.service.d" \
  "service.d"; do
  conflict_path="$systemd_search_root/$conflict_relative"
  case "$conflict_relative" in
    *.d | *.wants | *.requires | *.upholds) mkdir "$conflict_path" ;;
    *) touch "$conflict_path" ;;
  esac
  expect_rejected fresh_unit_conflict_preflight
  rm -rf -- "$conflict_path"
done

mkdir "$systemd_search_root/other.target.wants"
ln -s /nonexistent "$systemd_search_root/other.target.wants/$service_name"
expect_rejected fresh_unit_conflict_preflight
rm -rf -- "$systemd_search_root/other.target.wants"

previous_unit_link=$unit_link
previous_wants_link=$wants_link
unit_link="$systemd_search_root/$service_name"
wants_link="$systemd_search_root/multi-user.target.wants/$service_name"
mkdir "$systemd_search_root/multi-user.target.wants"
ln -s /nonexistent "$unit_link"
ln -s /nonexistent "$wants_link"
runtime_state=present
fresh_unit_conflict_preflight || test_fail "管理中のunit linkを競合と判定しました"
rm -f -- "$unit_link" "$wants_link"
rmdir "$systemd_search_root/multi-user.target.wants"
unit_link=$previous_unit_link
wants_link=$previous_wants_link

printf 'Installer contract test: ok\n'
