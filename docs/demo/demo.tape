# Records docs/assets/demo.gif. Run docs/demo/record.sh (task demo) from the
# repository root; it runs this tape inside docs/demo/Dockerfile.

Output docs/assets/demo.gif

Require trowel
Require nats-server
Require nats
Require curl
Require openssl
Require jq

Set Shell "bash"
Set FontSize 18
Set Width 1280
Set Height 800
Set Theme "Catppuccin Mocha"
Set Padding 24
Set TypingSpeed 35ms

Hide
Type `mkdir -p /tmp/trowel-demo && cd /vhs` Enter
Type `nats-server --jetstream --store_dir /tmp/trowel-demo/js >/tmp/trowel-demo/nats.log 2>&1 &` Enter
Sleep 1s
Type `nats stream add ORDERS --subjects 'orders.>,dlq.>' --defaults >/dev/null` Enter
Type `export ORDERS_SECRET=sample-orders-secret PS1='$ '` Enter
Type "clear" Enter
Show

Sleep 500ms
Type "cat docs/demo/trowel.yml" Enter
Sleep 5s

Ctrl+L
Sleep 300ms
Type `trowel -config docs/demo/trowel.yml -validate 2>&1 | jq -r .msg` Enter
Sleep 2s
Type `trowel -config docs/demo/trowel.yml 2>/tmp/trowel-demo/trowel.log &` Enter
Sleep 2s
Type `curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/readyz` Enter
Sleep 2s

Ctrl+L
Sleep 300ms
Type `body='{"id":"A-1001","total":42,"note":"dropped by the transform"}'` Enter
Sleep 500ms
Type `sig=$(printf %s "$body" | openssl dgst -sha256 -hmac "$ORDERS_SECRET" -r | cut -d' ' -f1)` Enter
Sleep 500ms
Type `curl -si localhost:8080/hooks/orders -H 'X-Event: created' -H "X-Signature: sha256=$sig" -d "$body" | head -n 1` Enter
Sleep 2s
Type `cat /tmp/trowel-demo/deliveries.txt` Enter
Sleep 3s

Type `curl -si localhost:8080/hooks/orders -H 'X-Event: created' -H 'X-Signature: sha256=forged' -d "$body" | head -n 1` Enter
Sleep 2s
Type `nats stream info ORDERS --json | jq .state.messages` Enter
Sleep 4s
