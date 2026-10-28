#!/usr/bin/env bash
# Drives the stack running in Kubernetes over HTTP. Every code carries a run id, so a kept Postgres volume never collides.
# Needs: curl, python3. The stack must be up: make cluster-up
set -euo pipefail

API="${API:-http://127.0.0.1:5229}"
RABBIT="${RABBIT:-http://127.0.0.1:15719}"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'

say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }

ADMIN_EMAIL="${ADMIN_EMAIL:-admin@mint.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-MintAdmin2026}"
CUSTOMER_PASSWORD="Smoke-Passw0rd1"
code_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }

open_drop() { # code unitsTotal -> prints nothing
  curl -sf -X POST "$API/api/drops" -H "$JSON" -H "$ADM" \
    -d "{\"code\":\"$1\",\"name\":\"Smoke Drop\",\"series\":\"Genesis\",\"artist\":\"Studio Aria\",\"unitsTotal\":$2,\"maxPerCollector\":$2,\"priceCents\":5000}" >/dev/null \
    || fail "could not open drop $1"
}
book() { # code reference units -> prints order id
  curl -sf -X POST "$API/api/orders" -H "$JSON" -H "${AS:-$CUS}" \
    -d "{\"dropCode\":\"$1\",\"reference\":\"$2\",\"units\":$3}" | jget "['id']"
}
order_field() { curl -sf -H "$ADM" "$API/api/orders/$1" | jget "['$2']"; }
remaining() { curl -sf -H "$ADM" "$API/api/drops/$1" | jget "['unitsLeft']"; }

wait_for() { # description command... : polls up to 30s until the command succeeds
  local what="$1"; shift
  for _ in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "timed out waiting for $what"
}
status_is() { [ "$(order_field "$1" status)" = "$2" ]; }
remaining_is() { [ "$(remaining "$1")" = "$2" ]; }
terminal() { case "$(order_field "$1" status)" in Sold|Rejected|Refunded) return 0 ;; *) return 1 ;; esac; }
event_types() { curl -sf -H "$ADM" "$API/api/orders/$1/events" | python3 -c "import sys,json; print(' '.join(e['type'] for e in json.load(sys.stdin)))"; }
dlq_is() { [ "$(dlq_count)" = "$1" ]; }
dlq_count() { curl -sf -u platform:platform "$RABBIT/api/queues/%2F/certificates.work.dlq" | jget "['messages']"; }

curl -sf "$API/api/health" >/dev/null || fail "API is not healthy at $API; run: make cluster-up"

echo "0. authentication and ownership"
[ "$(code_of "$API/api/drops")" = 401 ] || fail "an anonymous request should be 401"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-Passw0rd\"}")" = 401 ] || fail "a wrong password should be 401"
ADM="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | token_of)" || fail "the administrator could not sign in"
CUS="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"smoke-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)" || fail "could not register a customer"
OTHER="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"other-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -X POST "$API/api/drops" -H "$JSON" -H "$CUS" -d '{"code":"X"}')" = 403 ] || fail "a customer must not open drops"
open_drop "A-$RUN" 5
IDA="$(book "A-$RUN" "COL-$RUN-OWN" 1)"
[ "$(code_of -H "$OTHER" "$API/api/orders/$IDA")" = 404 ] || fail "another customer must get 404 for someone else's order"
[ "$(code_of -H "$CUS" "$API/api/orders/$IDA/events")" = 403 ] || fail "the event log is staff only"
[ "$(code_of -X POST -H "$OTHER" "$API/api/orders/$IDA/refund")" = 404 ] || fail "another customer must not refund it"
say "401 anonymous, 401 bad password, 403 customer opening a drop, 404 for another customer, 403 on the event log"

echo "0b. password reset ends the old sessions (needs the dev mailbox the demo stack turns on)"
RESET_EMAIL="reset-$RUN@example.com"
OLD="$(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 200 ] || fail "the new session should work"
[ "$(code_of -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"nobody-$RUN@example.com\"}")" = 202 ] || fail "an unknown address must look the same"
curl -sf -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\"}" >/dev/null || fail "forgot-password failed"
LINK="$(curl -sf "$API/api/dev/mailbox?to=$RESET_EMAIL" | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['link'])")"
link_param() { python3 -c "import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)['$1'][0])" "$LINK"; }
[ "$(code_of -X POST "$API/api/auth/reset-password" -H "$JSON" -d "{\"userId\":\"$(link_param reset)\",\"token\":\"$(link_param token)\",\"newPassword\":\"Another-Passw0rd9\"}")" = 204 ] || fail "reset failed"
sleep 3 # the session version is cached for a moment
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 401 ] || fail "the old access token should be revoked"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"Another-Passw0rd9\"}")" = 200 ] || fail "the new password should work"
say "reset link works once, the old access token is revoked, the new password signs in"

echo "1. input is validated at the door"
open_drop "V-$RUN" 10
for body in \
  "{\"dropCode\":\"V-$RUN\",\"reference\":\"\",\"units\":1}" \
  "{\"dropCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":0}" \
  "{\"dropCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":7}" \
  "{\"dropCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":4294967297}" \
  "{\"dropCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":9223372036854815807}"; do
  code="$(code_of -X POST "$API/api/orders" -H "$JSON" -H "$CUS" -d "$body")"
  [ "$code" = 400 ] || fail "expected 400 for $body, got $code"
done
say "empty, zero, over-limit and overflowing unit counts are all 400"

echo "2. happy path"
open_drop "H-$RUN" 20
ID="$(book "H-$RUN" "COL-$RUN-1" 4)"
wait_for "order to be Sold" status_is "$ID" Sold
[ "$(order_field "$ID" totalCents)" = 22400 ] || fail "total should be 4 x 5000 plus a 12% service fee = 22400"
[ "$(curl -sf -H "$ADM" "$API/api/certificates/$ID" | jget "['status']")" = Issued ] || fail "certificate not issued"
wait_for "remaining units to be 16" remaining_is "H-$RUN" 16
TYPES="$(event_types "$ID")"
for t in units.reserve units.held certificate.issued; do
  case "$TYPES" in *"$t"*) ;; *) fail "Kafka log is missing $t (has: $TYPES)" ;; esac
done
say "sold, total 22400, remaining 16, Kafka log has the three steps"

echo "3. last unit: three orders, one commits, two are short and write no hold"
open_drop "L-$RUN" 1
TMP="$(mktemp -d)"
# three different collectors, so the stock (not the per-collector limit) is what says no
for n in A B C; do
  case $n in A) WHO="$ADM" ;; B) WHO="$CUS" ;; C) WHO="$OTHER" ;; esac
  ( AS="$WHO" book "L-$RUN" "COL-$RUN-$n" 1 >"$TMP/$n" ) &
done
wait
IDS=""
for n in A B C; do [ -s "$TMP/$n" ] || fail "order $n was not accepted"; IDS="$IDS $(cat "$TMP/$n")"; done
rm -rf "$TMP"
for i in $IDS; do wait_for "order $i to settle" terminal "$i"; done
SOLD=0; SHORT=0
for i in $IDS; do
  s="$(order_field "$i" status)"
  if [ "$s" = Sold ]; then SOLD=$((SOLD + 1)); fi
  if [ "$s" = Rejected ]; then
    case "$(order_field "$i" reason)" in Short*) SHORT=$((SHORT + 1)); case "$(event_types "$i")" in *units.held*) fail "the short loser has a hold" ;; esac ;; esac
  fi
done
[ "$SOLD" = 1 ] && [ "$SHORT" = 2 ] || fail "expected 1 sold and 2 short, got $SOLD sold and $SHORT short"
[ "$(remaining "L-$RUN")" = 0 ] || fail "remaining should be 0"
say "1 sold, 2 short, remaining 0, no hold for the losers"

echo "3b. per-collector limit: the third unit for one collector is refused, the stock is untouched"
curl -sf -X POST "$API/api/drops" -H "$JSON" -H "$ADM" \
  -d "{\"code\":\"C-$RUN\",\"name\":\"Smoke Drop\",\"series\":\"Genesis\",\"artist\":\"Studio Aria\",\"unitsTotal\":10,\"maxPerCollector\":2,\"priceCents\":5000}" >/dev/null \
  || fail "could not open the capped drop"
CAP1="$(book "C-$RUN" "COL-$RUN-C1" 2)"
wait_for "first capped order to be Sold" status_is "$CAP1" Sold
CAP2="$(book "C-$RUN" "COL-$RUN-C2" 1)"
wait_for "capped order to settle" terminal "$CAP2"
[ "$(order_field "$CAP2" status)" = Rejected ] || fail "the third unit for one collector should be rejected"
case "$(order_field "$CAP2" reason)" in Cap*) ;; *) fail "the reason should say cap, got: $(order_field "$CAP2" reason)" ;; esac
case "$(event_types "$CAP2")" in *units.held*) fail "the capped order has a hold" ;; esac
[ "$(remaining "C-$RUN")" = 8 ] || fail "remaining should be 8 after the refused order"
[ "$(code_of -X POST "$API/api/drops" -H "$JSON" -H "$ADM" -d "{\"code\":\"X-$RUN\",\"name\":\"x\",\"series\":\"x\",\"artist\":\"x\",\"unitsTotal\":3,\"maxPerCollector\":9,\"priceCents\":5000}")" = 400 ] || fail "a cap above the units on sale must be refused"
say "2 units sold to one collector, the next refused as Cap, remaining 8, no hold"

echo "4. poison: committed as rejected, dead-lettered, the units come back"
open_drop "P-$RUN" 5
BEFORE="$(dlq_count)"
IDP="$(book "P-$RUN" "COL-POISON-$RUN" 2)"
wait_for "poison order to be Rejected" status_is "$IDP" Rejected
wait_for "units to be restored to 5" remaining_is "P-$RUN" 5
wait_for "certificates.work.dlq to gain one message" dlq_is $((BEFORE + 1))
[ "$(curl -sf -H "$ADM" "$API/api/certificates/$IDP" | jget "['status']")" = Rejected ] || fail "rejection should be committed on the certificate row"
[ "$(order_field "$IDP" unitsReleasedAt)" != None ] || fail "order should record the release"
say "rejected, dead-lettered (+1 in certificates.work.dlq), remaining back to 5"

echo "4b. dead letters can be listed by staff only, and replayed"
[ "$(code_of -H "$CUS" "$API/api/admin/dead-letters")" = 403 ] || fail "dead letters are staff only"
curl -sf -H "$ADM" "$API/api/admin/dead-letters" | python3 -c "import sys,json; q={x['queue']:x for x in json.load(sys.stdin)}; assert q['certificates.work']['count']>=1, q" || fail "the dead-letter queue should list the poison message"
BEFORE_REPLAY="$(dlq_count)"
REPLAYED="$(curl -sf -X POST -H "$ADM" "$API/api/admin/dead-letters/certificates.work/replay?max=100" | jget "['replayed']")" || fail "replay failed"
[ "$REPLAYED" -ge 1 ] || fail "replay should have taken at least one message"
# The poison reference still fails for the same reason, so it is retried and dead-lettered again: replay is for fixed causes.
wait_for "the replayed poison message to be dead-lettered again" dlq_is "$BEFORE_REPLAY"
say "listed, replayed $REPLAYED, dead-lettered again because the cause is still there"

echo "5. refund races the final mark: the refund wins, the issued certificate stays"
open_drop "R-$RUN" 6
WON=0
for attempt in 1 2 3; do
  IDR="$(book "R-$RUN" "COL-$RUN-R$attempt" 2)"
  # The certificate exists before the order is marked Sold (the final mark is delayed in the demo cluster).
  wait_for "certificate to exist" curl -sf -H "$ADM" "$API/api/certificates/$IDR"
  if status_is "$IDR" Held; then WON=1; break; fi
  say "window missed on attempt $attempt (order already Sold); the refund still has to hold; retrying"
  curl -sf -X POST -H "$CUS" "$API/api/orders/$IDR/refund" >/dev/null || true
  wait_for "order to be Refunded" status_is "$IDR" Refunded
done
[ "$WON" = 1 ] || fail "never caught the order between certificate issued and final mark"
curl -sf -X POST -H "$CUS" "$API/api/orders/$IDR/refund" >/dev/null || fail "refund was refused"
wait_for "order to be Refunded" status_is "$IDR" Refunded
sleep 3 # longer than the final-mark delay: the late mark must not undo the refund
status_is "$IDR" Refunded || fail "the final mark overwrote the refund"
[ "$(curl -sf -H "$ADM" "$API/api/certificates/$IDR" | jget "['status']")" = Issued ] || fail "the issued certificate was rolled back"
[ "$(order_field "$IDR" certificateNumber)" = None ] || fail "the late final mark should have been skipped"
wait_for "units to come back" remaining_is "R-$RUN" 6
code="$(code_of -X POST -H "$CUS" "$API/api/orders/$IDR/refund")"
[ "$code" = 409 ] || fail "second refund should be 409, got $code"
say "order Refunded, certificate still Issued, remaining 6, second refund 409"

echo "golang-mint smoke passed"
