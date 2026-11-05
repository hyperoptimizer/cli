#!/bin/sh
set -eu
binary=${1:?Usage: install.sh /path/to/hpo /installation/directory}
destination=${2:?Usage: install.sh /path/to/hpo /installation/directory}
[ -f "$binary" ] || { echo 'Built binary not found' >&2; exit 1; }
for name in hpo hyperoptimizer; do
  if [ -e "$destination/$name" ] || [ -L "$destination/$name" ]; then
    echo "Refusing to overwrite $destination/$name" >&2
    exit 1
  fi
done
mkdir -p "$destination"
# Do not overwrite a concurrently installed file.
(umask 022; set -C; cat "$binary" > "$destination/hpo")
chmod 755 "$destination/hpo"
ln -s hpo "$destination/hyperoptimizer"
echo "Installed hpo and hyperoptimizer in $destination"
