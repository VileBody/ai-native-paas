#!/usr/bin/env sh
set -eu
# Creates the filesystem shape used by source-safety tests. Kept as a script
# because archives and Windows checkouts do not preserve malicious symlinks.
ln -s ../../outside escape
