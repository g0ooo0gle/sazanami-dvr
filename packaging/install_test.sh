#!/bin/sh
set -eu

script_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
installer="$script_root/install.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/sazanami-install-test.XXXXXX")

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

fail() {
  printf 'Installer contract testに失敗しました: %s\n' "$*" >&2
  exit 1
}

expect_rejected() {
  if "$@" >/dev/null 2>&1; then
    fail "拒否される入力を受理しました: $*"
  fi
}

[ -f "$installer" ] || fail "installer-missing"
[ -x "$installer" ] || fail "installer-not-executable"
sh -n "$installer"

for arguments in '' 'unknown' 'install extra' 'uninstall extra' 'purge extra'; do
  output="$test_root/usage.out"
  if [ -n "$arguments" ]; then
    # shellcheck disable=SC2086 -- split is intentional for CLI argument tests.
    if sh "$installer" $arguments >"$output" 2>&1; then
      fail "usage errorが成功しました: $arguments"
    fi
  elif sh "$installer" >"$output" 2>&1; then
    fail "引数なしが成功しました"
  fi
  grep -F 'usage:' "$output" >/dev/null || fail "usageを表示しませんでした: $arguments"
done

# Source only the pure contract helpers. install.sh runs main only when invoked directly.
. "$installer"

for accepted_path in /var/lib/sazanami-dvr /var/lib/sazanami-dvr/recordings /srv/recordings-01; do
  valid_literal_path "$accepted_path" || fail "正常pathを拒否しました: $accepted_path"
done

for rejected_path in '' relative /tmp/../var /tmp/./var /tmp//var '/tmp/a b' '/tmp/$value' '/tmp/*' '/tmp/a\b' '/tmp/`id`'; do
  expect_rejected valid_literal_path "$rejected_path"
done

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
[ "$purge_data_root" = /var/lib/sazanami-dvr ] || fail "data rootの解析結果が違います"
[ "$purge_recording_root" = /srv/recordings ] || fail "recording rootの解析結果が違います"
[ ! -e "$not_sourced" ] || fail "環境設定をsourceしました"

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
[ "$marker_format" = 1 ] || fail "marker formatが違います"
[ "$marker_installation_id" = 0123456789abcdef0123456789abcdef ] || fail "marker idが違います"
[ "$marker_uid" = 123 ] || fail "marker uidが違います"
[ "$marker_gid" = 456 ] || fail "marker gidが違います"

invalid_marker="$test_root/invalid.marker"
{
  printf '%s\n' 'format=1'
  printf '%s\n' 'installation_id=0123456789abcdef0123456789abcdef'
  printf '%s\n' 'installation_id=ffffffffffffffffffffffffffffffff'
  printf '%s\n' 'uid=123'
  printf '%s\n' 'gid=456'
} >"$invalid_marker"
expect_rejected read_account_marker "$invalid_marker"

printf 'Installer contract test: ok\n'
