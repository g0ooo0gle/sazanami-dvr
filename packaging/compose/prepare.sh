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
