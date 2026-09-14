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

synthetic_root=$(mktemp -d)
synthetic_port=$(python3 - <<'PY'
import socket

with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
python3 "$script_root/synthetic_mirakurun.py" "$synthetic_port" >"$synthetic_root/output" 2>"$synthetic_root/error" &
synthetic_pid=$!
trap 'kill "$synthetic_pid" 2>/dev/null || true; wait "$synthetic_pid" 2>/dev/null || true; rm -rf -- "$synthetic_root"' EXIT HUP INT TERM
python3 - "$synthetic_port" <<'PY'
import sys
import time
import urllib.request

port = int(sys.argv[1])
deadline = time.monotonic() + 5
while True:
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/api/version", timeout=1) as response:
            response.read()
        break
    except OSError:
        if time.monotonic() >= deadline:
            raise
        time.sleep(0.1)
PY
python3 - "$synthetic_port" <<'PY'
import sys
import urllib.request

port = int(sys.argv[1])
request = urllib.request.Request(
    f"http://127.0.0.1:{port}/api/services/100003/stream?decode=0",
    headers={"Accept": "video/MP2T", "X-Mirakurun-Priority": "0"},
)
with urllib.request.urlopen(request, timeout=2) as response:
    assert response.status == 200
    assert response.headers.get_content_type() == "video/mp2t"
    packet = response.read(188)
assert len(packet) == 188
assert packet[0] == 0x47
assert packet[1] & 0x40
assert packet[4] == 0
assert packet[5] == 0
section = packet[5:21]
crc = 0xFFFFFFFF
for value in section[:-4]:
    crc ^= value << 24
    for _ in range(8):
        crc = ((crc << 1) ^ 0x04C11DB7) & 0xFFFFFFFF if crc & 0x80000000 else (crc << 1) & 0xFFFFFFFF
assert crc == int.from_bytes(section[-4:], "big")
assert packet[8:10] == b"\x00\x02"
assert packet[13:15] == b"\x00\x03"
PY
kill "$synthetic_pid"
wait "$synthetic_pid" 2>/dev/null || true
rm -rf -- "$synthetic_root"
synthetic_root=
trap - EXIT HUP INT TERM

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
