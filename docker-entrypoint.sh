#!/bin/sh
set -eu

# The production knowledge directory is a persistent volume. Older volumes
# may have been created before bundled standards were added to the image, so
# copy only missing seed files into the volume and never overwrite uploads.
seed_dir="/app/knowledge-seed"
knowledge_dir="/app/docs/knowledge"
mkdir -p "$knowledge_dir"
if [ -d "$seed_dir" ]; then
  for source in "$seed_dir"/*; do
    [ -f "$source" ] || continue
    name=$(basename "$source")
    [ "$name" = ".index-manifest.json" ] && continue
    target="$knowledge_dir/$name"
    if [ ! -e "$target" ]; then
      cp "$source" "$target"
    fi
  done
fi

exec /app/eino-server "$@"
