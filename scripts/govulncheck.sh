#!/usr/bin/env bash
# Runs govulncheck and fails on any vulnerability the code reaches, except
# the one excluded below.
#
# GO-2026-6443 (google.golang.org/grpc v1.84.0): a server configured with xDS
# routing panics on a request with neither :authority nor Host, because the
# xDS routing interceptor indexes the empty authority. This service serves
# plain gRPC and never configures xDS, so the panic path is not reachable;
# govulncheck still reports it because grpc.Server.Serve leads to the HTTP/2
# transport. Tracked in https://github.com/Steward-GRC/steward-core/issues/13.
# Remove when grpc 1.84.1+ or 1.85 ships and is bumped.
#
# Usage: scripts/govulncheck.sh [path to govulncheck]
set -euo pipefail

govulncheck="${1:-govulncheck}"
excluded="GO-2026-6443"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT
"$govulncheck" -format json ./... >"$out"

# A finding whose first trace frame names a function is reachable from this
# module's code; module- and package-level findings are reported, not failed.
called="$(jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' "$out" | sort -u)"
failing="$(grep -vx "$excluded" <<<"$called" || true)"

if grep -qx "$excluded" <<<"$called"; then
  echo "govulncheck: $excluded reached but excluded (xDS routing only; see this script)"
fi
if [[ -n "$failing" ]]; then
  echo "govulncheck: reachable vulnerabilities:" >&2
  echo "$failing" >&2
  "$govulncheck" ./... >&2 || true
  exit 1
fi
echo "govulncheck: no reachable vulnerabilities outside the exclusion"
