#!/bin/bash
# run.sh [phase] — site.sh up, checks.py [phase], and ALWAYS site.sh down,
# even when up or a check fails. Exit status is checks.py's (or up's).
# Starts from a clean site unless E2E_KEEP_STATE=1.
set -uo pipefail
T="$(cd "$(dirname "$0")" && pwd)"
trap '"$T/site.sh" down' EXIT
if [ "${E2E_KEEP_STATE:-}" != 1 ]; then
    "$T/site.sh" clean || exit $?
fi
"$T/site.sh" up || exit $?
python3 -u "$T/checks.py" "${1:-all}"
