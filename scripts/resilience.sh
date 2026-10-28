#!/usr/bin/env bash
# What Kubernetes buys: kill an API replica and the RabbitMQ pod in the middle of a drop and check that nothing is lost or
# done twice. Orders are accepted, the stock is never oversold, two of three orders for two units each are sold, and every
# message the broker held comes back from its persistent volume. Needs the cluster from `make cluster-up`.
set -euo pipefail

API="${API:-http://127.0.0.1:5229}"
CTX="${CTX:-kind-golang-mint}"
K="kubectl --context $CTX -n mint"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'
say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }
# every call retries a few times: for a moment after a pod dies the Service may still send a request to it
retry() { for _ in $(seq 1 20); do if out="$("$@" 2>/dev/null)"; then printf '%s' "$out"; return 0; fi; sleep 1; done; return 1; }

login() { retry curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"admin@mint.local\",\"password\":\"${ADMIN_PASSWORD:-MintAdmin2026}\"}" | token_of; }
register() { retry curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$1-$RUN@example.com\",\"password\":\"Resil-Passw0rd1\"}" | token_of; }
ADM="Authorization: Bearer $(login)"
A="Authorization: Bearer $(register a)"
B="Authorization: Bearer $(register b)"
C="Authorization: Bearer $(register c)"

echo "1. a drop of four units, two per collector at most, and two orders (two units each) in flight"
retry curl -sf -X POST "$API/api/drops" -H "$JSON" -H "$ADM" -d "{\"code\":\"RES-$RUN\",\"name\":\"Resilience\",\"series\":\"Genesis\",\"artist\":\"Studio Aria\",\"unitsTotal\":4,\"priceCents\":5000,\"maxPerCollector\":2}" >/dev/null || fail "could not open the drop"
order() { retry curl -sf -X POST "$API/api/orders" -H "$JSON" -H "$1" -d "{\"dropCode\":\"RES-$RUN\",\"reference\":\"COL-$RUN-$2\",\"units\":2}" | jget "['id']"; }
IDA="$(order "$A" a)"; IDB="$(order "$B" b)"

echo "2. kill an API replica and the RabbitMQ pod while the orders are still in flight"
$K delete "$($K get pods -l app.kubernetes.io/name=api -o name | head -1)" --wait=false | sed 's/^/  /'
$K delete pod rabbitmq-0 --wait=false | sed 's/^/  /'
IDC="$(order "$C" c)" # accepted even now: the order and its outbox row are one local transaction
say "a third order was accepted while the broker was down"

echo "3. everything comes back: two orders are sold, the third is told how many were left, and the stock was never oversold"
$K rollout status statefulset/rabbitmq --timeout=180s >/dev/null
$K rollout status deployment/api --timeout=180s >/dev/null
status() { body="$(retry curl -sf -H "$1" "$API/api/orders/$2")" && printf '%s' "$body" | jget "['status']" || echo unknown; }
settled() { s="$(status "$1" "$2")"; [ "$s" = Sold ] || [ "$s" = Rejected ]; }
for _ in $(seq 1 240); do
  if settled "$A" "$IDA" && settled "$B" "$IDB" && settled "$C" "$IDC"; then break; fi
  sleep 1
done
SOLD=0; SHORT=0
for pair in "$A|$IDA" "$B|$IDB" "$C|$IDC"; do
  s="$(status "${pair%%|*}" "${pair##*|}")"
  case "$s" in Sold) SOLD=$((SOLD + 1)) ;; Rejected) SHORT=$((SHORT + 1)) ;; *) fail "an order is stuck in $s" ;; esac
done
[ "$SOLD" = 2 ] && [ "$SHORT" = 1 ] || fail "expected 2 sold and 1 short, got $SOLD and $SHORT"
[ "$(retry curl -sf -H "$ADM" "$API/api/drops/RES-$RUN" | jget "['unitsLeft']")" = 0 ] || fail "all four units must be held, and no more"
CERTS=0
for id in "$IDA" "$IDB" "$IDC"; do
  if curl -sf -H "$ADM" "$API/api/certificates/$id" >/dev/null 2>&1; then CERTS=$((CERTS + 1)); fi
done
[ "$CERTS" = 2 ] || fail "exactly two certificates may exist, found $CERTS"
say "2 sold, 1 short, 0 units left, 2 certificates"

echo "4. the cap holds after the failures: a sold collector cannot take one more"
for pair in "$A|$IDA" "$B|$IDB" "$C|$IDC"; do
  if [ "$(status "${pair%%|*}" "${pair##*|}")" = Sold ]; then WHO="${pair%%|*}"; break; fi
done
retry curl -sf -X POST "$API/api/drops" -H "$JSON" -H "$ADM" -d "{\"code\":\"CAP-$RUN\",\"name\":\"Cap\",\"series\":\"Genesis\",\"artist\":\"Studio Aria\",\"unitsTotal\":10,\"priceCents\":5000,\"maxPerCollector\":1}" >/dev/null || fail "could not open the cap drop"
O1="$(retry curl -sf -X POST "$API/api/orders" -H "$JSON" -H "$WHO" -d "{\"dropCode\":\"CAP-$RUN\",\"reference\":\"COL-$RUN-cap1\",\"units\":1}" | jget "['id']")"
O2="$(retry curl -sf -X POST "$API/api/orders" -H "$JSON" -H "$WHO" -d "{\"dropCode\":\"CAP-$RUN\",\"reference\":\"COL-$RUN-cap2\",\"units\":1}" | jget "['id']")"
for _ in $(seq 1 120); do
  if settled "$WHO" "$O1" && settled "$WHO" "$O2"; then break; fi
  sleep 1
done
R1="$(status "$WHO" "$O1")"; R2="$(status "$WHO" "$O2")"
{ [ "$R1" = Sold ] && [ "$R2" = Rejected ]; } || { [ "$R1" = Rejected ] && [ "$R2" = Sold ]; } || fail "a cap of 1: one sold and one refused, got $R1 and $R2"
[ "$(retry curl -sf -H "$ADM" "$API/api/drops/CAP-$RUN" | jget "['unitsLeft']")" = 9 ] || fail "the refused order must take no stock"
say "a cap of 1: one sold, one refused, 9 units left"
echo "resilience passed"
