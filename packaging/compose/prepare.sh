#!/bin/sh

set -eu

base_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
uid=$(id -u)
gid=$(id -g)

umask 077

for relative in data/sazanami data/konomitv data/konomitv/home data/konomitv/cache logs/konomitv recordings captures config; do
    path="$base_dir/$relative"
    if [ -L "$path" ]; then
        printf '%s\n' "symlinkは使えません: $relative" >&2
        exit 1
    fi
    mkdir -p -- "$path"
    chmod 0700 "$path"
done

lock_path="$base_dir/recordings/.sazanami-dvr.lock"
if [ ! -e "$lock_path" ] && [ ! -L "$lock_path" ]; then
    if ! (set -C; : > "$lock_path") 2>/dev/null && [ ! -e "$lock_path" ] && [ ! -L "$lock_path" ]; then
        printf '%s\n' '録画lockを作成できませんでした。' >&2
        exit 1
    fi
fi

if [ -L "$lock_path" ]; then
    printf '%s\n' '録画lockが不正です: symlink' >&2
    exit 1
fi
if [ ! -f "$lock_path" ]; then
    printf '%s\n' '録画lockが不正です: not-regular' >&2
    exit 1
fi
if values=$(stat -c '%u %a %h' "$lock_path" 2>/dev/null); then
    :
elif values=$(stat -f '%u %Lp %l' "$lock_path" 2>/dev/null); then
    :
else
    printf '%s\n' '録画lockが不正です: stat' >&2
    exit 1
fi
set -- $values
if [ "$1" != "$uid" ]; then
    printf '%s\n' '録画lockが不正です: owner' >&2
    exit 1
fi
if [ "$2" != 600 ]; then
    printf '%s\n' '録画lockが不正です: mode' >&2
    exit 1
fi
if [ "$3" != 1 ]; then
    printf '%s\n' '録画lockが不正です: link-count' >&2
    exit 1
fi

if [ ! -e "$base_dir/.env" ]; then
    sed -e "s/^HOST_UID=.*/HOST_UID=$uid/" -e "s/^HOST_GID=.*/HOST_GID=$gid/" \
        "$base_dir/.env.example" > "$base_dir/.env"
    chmod 0600 "$base_dir/.env"
fi

if [ ! -e "$base_dir/config/konomitv.yaml" ]; then
    cp "$base_dir/konomitv.yaml.example" "$base_dir/config/konomitv.yaml"
    chmod 0600 "$base_dir/config/konomitv.yaml"
fi

printf '%s\n' '準備が完了しました。channels.jsonと接続先を設定してから明示DB操作を実行してください。'
