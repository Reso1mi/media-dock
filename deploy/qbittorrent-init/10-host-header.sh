#!/bin/sh
set -eu

config_dir=/config/qBittorrent
config_file="$config_dir/qBittorrent.conf"
mkdir -p "$config_dir"

if [ ! -f "$config_file" ]; then
  printf '%s\n' '[Preferences]' 'WebUI\HostHeaderValidation=false' > "$config_file"
elif grep -q '^WebUI\\HostHeaderValidation=' "$config_file"; then
  sed -i 's/^WebUI\\HostHeaderValidation=.*/WebUI\\HostHeaderValidation=false/' "$config_file"
else
  printf '%s\n' 'WebUI\HostHeaderValidation=false' >> "$config_file"
fi

chown -R "${PUID:-1000}:${PGID:-1000}" "$config_dir"
