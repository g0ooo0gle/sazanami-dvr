#!/bin/sh

set -eu

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/sazanami-compose-prepare.XXXXXX")
trap 'rm -r -- "$work_dir"' EXIT HUP INT TERM

copy_fixture() {
    fixture=$1
    mkdir -p -- "$fixture"
    cp "$source_dir/prepare.sh" "$source_dir/.env.example" "$source_dir/konomitv.yaml.example" "$fixture/"
}

file_values() {
    if stat -c '%u %a %h %i' "$1" >/dev/null 2>&1; then
        stat -c '%u %a %h %i' "$1"
    else
        stat -f '%u %Lp %l %i' "$1"
    fi
}

expect_invalid() {
    fixture=$1
    reason=$2
    before=$(file_values "$fixture/recordings/.sazanami-dvr.lock" 2>/dev/null || true)
    if (cd "$fixture" && sh ./prepare.sh) >"$fixture/output" 2>"$fixture/error"; then
        printf '%s\n' "不正なlockを受理しました: $reason" >&2
        exit 1
    fi
    grep -Fqx "録画lockが不正です: $reason" "$fixture/error"
    after=$(file_values "$fixture/recordings/.sazanami-dvr.lock" 2>/dev/null || true)
    test "$before" = "$after"
}

normal="$work_dir/normal"
copy_fixture "$normal"
(cd "$normal" && sh ./prepare.sh >/dev/null)
lock="$normal/recordings/.sazanami-dvr.lock"
test -f "$lock"
test ! -L "$lock"
set -- $(file_values "$lock")
test "$1" = "$(id -u)"
test "$2" = 600
test "$3" = 1
inode=$4
(cd "$normal" && sh ./prepare.sh >/dev/null)
set -- $(file_values "$lock")
test "$2" = 600
test "$3" = 1
test "$4" = "$inode"

symlink="$work_dir/symlink"
copy_fixture "$symlink"
mkdir -p "$symlink/recordings"
ln -s target "$symlink/recordings/.sazanami-dvr.lock"
expect_invalid "$symlink" symlink

directory="$work_dir/directory"
copy_fixture "$directory"
mkdir -p "$directory/recordings/.sazanami-dvr.lock"
expect_invalid "$directory" not-regular

fifo="$work_dir/fifo"
copy_fixture "$fifo"
mkdir -p "$fifo/recordings"
mkfifo "$fifo/recordings/.sazanami-dvr.lock"
expect_invalid "$fifo" not-regular

mode="$work_dir/mode"
copy_fixture "$mode"
mkdir -p "$mode/recordings"
: > "$mode/recordings/.sazanami-dvr.lock"
chmod 0644 "$mode/recordings/.sazanami-dvr.lock"
expect_invalid "$mode" mode

links="$work_dir/links"
copy_fixture "$links"
mkdir -p "$links/recordings"
: > "$links/recordings/.sazanami-dvr.lock"
chmod 0600 "$links/recordings/.sazanami-dvr.lock"
ln "$links/recordings/.sazanami-dvr.lock" "$links/recordings/other"
expect_invalid "$links" link-count

owner="$work_dir/owner"
copy_fixture "$owner"
(cd "$owner" && sh ./prepare.sh >/dev/null)
mkdir -p "$owner/fake-bin"
real_id=$(command -v id)
cat > "$owner/fake-bin/id" <<EOF
#!/bin/sh
if [ "\${1:-}" = -u ]; then
    echo $(( $(id -u) + 1 ))
else
    exec "$real_id" "\$@"
fi
EOF
chmod 0700 "$owner/fake-bin/id"
before=$(file_values "$owner/recordings/.sazanami-dvr.lock")
if (cd "$owner" && PATH="$owner/fake-bin:$PATH" sh ./prepare.sh) >"$owner/output" 2>"$owner/error"; then
    printf '%s\n' '別所有者のlockを受理しました' >&2
    exit 1
fi
grep -Fqx '録画lockが不正です: owner' "$owner/error"
test "$before" = "$(file_values "$owner/recordings/.sazanami-dvr.lock")"

printf '%s\n' 'prepare.shの録画lock検査に成功しました。'
