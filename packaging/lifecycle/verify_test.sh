#!/bin/sh
set -eu

script_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$script_root/verify.sh"

for rejected in '' / /var /home ../relative '/tmp/*' /tmp/not-owned; do
  if (check_purge_target "$rejected") >/dev/null 2>&1; then
    printf 'purge範囲外を拒否しませんでした: %s\n' "$rejected" >&2
    exit 1
  fi
done

for accepted in /opt/sazanami-dvr /etc/sazanami-dvr /var/lib/sazanami-dvr; do
  check_purge_target "$accepted"
done

ownership_root=$(mktemp -d)
trap 'rm -rf -- "$ownership_root"' EXIT HUP INT TERM
mkdir -p "$ownership_root/install/v1/packaging/systemd" "$ownership_root/outside"
touch "$ownership_root/install/v1/packaging/systemd/sazanami-dvr.service"
ln -s "$ownership_root/install/v1/packaging/systemd/sazanami-dvr.service" "$ownership_root/unit"
unit_link_targets_release "$ownership_root/unit" "$ownership_root/install"
rm -f -- "$ownership_root/unit"
ln -s "$ownership_root/outside/sazanami-dvr.service" "$ownership_root/unit"
if unit_link_targets_release "$ownership_root/unit" "$ownership_root/install"; then
  printf '管理外unit linkを所有済みと判定しました\n' >&2
  exit 1
fi
rm -rf -- "$ownership_root"
ownership_root=
trap - EXIT HUP INT TERM

grep -F 'retention-sentinel' "$script_root/verify.sh" >/dev/null
[ -x "$script_root/../install.sh" ]
[ -x "$script_root/installer_verify.sh" ]
sh -n "$script_root/../install.sh"
sh -n "$script_root/installer_verify.sh"
grep -F 'validate_candidate_archive "$candidate_archive"' "$script_root/installer_verify.sh" >/dev/null

repository_root=$(CDPATH= cd -- "$script_root/../.." && pwd)
for workflow in ci.yml release.yml; do
  grep -F 'sudo chown root:root /opt' "$repository_root/.github/workflows/$workflow" >/dev/null
  grep -F 'sudo chmod 0755 /opt' "$repository_root/.github/workflows/$workflow" >/dev/null
  grep -F 'sudo chown root:root /usr/local/bin' "$repository_root/.github/workflows/$workflow" >/dev/null
  grep -F 'sudo chmod 0755 /usr/local/bin' "$repository_root/.github/workflows/$workflow" >/dev/null
done

python3 -c 'compile(open("'"$script_root"'/synthetic_mirakurun.py", encoding="utf-8").read(), "synthetic_mirakurun.py", "exec")'
sh -n "$script_root/verify.sh"

if (
  . "$script_root/installer_verify.sh"
  wants_root=/tmp/sazanami-lifecycle-wants-root
  path_exists() { return 0; }
  root_controlled_directory() { return 0; }
  wants_root_is_mountpoint() { return 1; }
  wants_root_preflight
); then
  :
else
  printf '安全なwants rootを受理できませんでした\n' >&2
  exit 1
fi

if (
  . "$script_root/installer_verify.sh"
  wants_root=/tmp/sazanami-lifecycle-wants-root
  path_exists() { return 0; }
  root_controlled_directory() { return 1; }
  wants_root_is_mountpoint() { return 1; }
  wants_root_preflight
) >/dev/null 2>&1; then
  printf '管理外wants rootを拒否しませんでした\n' >&2
  exit 1
fi

if (
  . "$script_root/installer_verify.sh"
  wants_root=/tmp/sazanami-lifecycle-wants-root
  path_exists() { return 0; }
  root_controlled_directory() { return 0; }
  wants_root_is_mountpoint() { return 0; }
  wants_root_preflight
) >/dev/null 2>&1; then
  printf 'mountされたwants rootを拒否しませんでした\n' >&2
  exit 1
fi

printf 'Linux lifecycle safety contract: ok\n'
