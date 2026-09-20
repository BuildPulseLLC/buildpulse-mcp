#!/usr/bin/env bash
# Who is actually connecting to the hosted MCP, and are they getting through?
#
# Reads the mcp-remote task logs rather than a live mcpSessions count: that
# collection has a TTL index on expiresAt and access tokens last an hour, so
# it self-empties and a count of 0 says nothing about whether MCP is used.
#
#   ./scripts/mcp-adoption.sh [hours]   (default 24)
set -euo pipefail
HOURS="${1:-24}"
: "${AWS_PROFILE:=bp-prod}"; export AWS_PROFILE
REGION=us-west-2
LG=/aws/ecs/production
START=$(python3 -c "import time;print(int((time.time()-${HOURS}*3600)*1000))")

STREAMS=$(aws logs describe-log-streams --log-group-name "$LG" --region "$REGION" \
  --log-stream-name-prefix mcp-remote --max-items 100 \
  --query 'logStreams[].[logStreamName,lastEventTimestamp]' --output text \
  | sort -k2 -rn | head -6 | awk '{print $1}')

TMP=$(mktemp)
for S in $STREAMS; do
  aws logs get-log-events --log-group-name "$LG" --region "$REGION" --log-stream-name "$S" \
    --start-time "$START" --limit 2000 --query 'events[].message' --output text 2>/dev/null \
    | tr '\t' '\n' >> "$TMP" || true
done

echo "=== hosted MCP adoption, last ${HOURS}h (prod) ==="
printf "  new client registrations : %s\n" "$(grep -c 'POST /oauth/register -> 201' "$TMP" || true)"
printf "  consent screens shown    : %s\n" "$(grep -c 'awaiting consent' "$TMP" || true)"
printf "  consent APPROVED         : %s\n" "$(grep -c 'consent: approved' "$TMP" || true)"
printf "  consent DENIED           : %s\n" "$(grep -c 'denied by user' "$TMP" || true)"
printf "  sessions persisted OK    : %s\n" "$(grep -c 'persistMCPSession ok' "$TMP" || true)"
printf "  sessions FAILED          : %s\n" "$(grep -c 'persistMCPSession failed' "$TMP" || true)"
printf "  authenticated tool calls : %s\n" "$(grep -c 'POST /mcp -> 200' "$TMP" || true)"

echo
echo "--- who reached consent (client name x count) ---"
grep 'awaiting consent' "$TMP" | sed -E 's/.*client=[^ ]+ \((.*)\) recognised=.*/\1/' \
  | sort | uniq -c | sort -rn | head -10 | sed 's/^/  /' || echo "  none"

echo
echo "--- who completed (email x orgs) ---"
grep 'persistMCPSession ok' "$TMP" | sed -E 's/.*ok for ([^ ]+) \(([0-9]+) orgs\).*/\1  \2 orgs/' \
  | sort | uniq -c | sort -rn | head -10 | sed 's/^/  /' || echo "  none"

echo
echo "--- stuck: reached consent but never persisted a session ---"
A=$(grep -c 'consent: approved' "$TMP" || echo 0); P=$(grep -c 'persistMCPSession ok' "$TMP" || echo 0)
echo "  approved=$A persisted=$P  gap=$((A-P))   (gap > 0 means codes issued that were never redeemed)"
rm -f "$TMP"
