#!/bin/sh
# Builds an isolated demo workspace and state under /tmp/tgx-shots.
set -e
B=/tmp/tgx-shots
rm -rf "$B/home" "$B/wiz" "$B/frames" "$B/cli"
mkdir -p "$B/frames"
P=/tmp/demo/shop-api
W=/tmp/demo-new/shop-api
rm -rf /tmp/demo /tmp/demo-new
mkdir -p "$P/internal/api" "$P/scripts" "$B/home/.termixgo" "$B/wiz/.termixgo" "$W"

cat > "$P/go.mod" <<'EOF'
module shop-api

go 1.26
EOF

cat > "$P/main.go" <<'EOF'
package main

import (
	"log"
	"net/http"

	"shop-api/internal/api"
)

func main() {
	mux := http.NewServeMux()
	api.RegisterOrders(mux, api.NewStore())
	log.Fatal(http.ListenAndServe(":8080", mux))
}
EOF

cat > "$P/internal/api/orders.go" <<'EOF'
package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// RegisterOrders mounts the orders endpoints.
func RegisterOrders(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("/orders", ordersHandler(store))
}

func ordersHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.List(time.Now()))
	}
}
EOF

cat > "$P/scripts/test.sh" <<'EOF'
#!/bin/sh
echo "=== RUN   TestOrdersList"
sleep 2
echo "--- PASS: TestOrdersList (0.00s)"
echo "=== RUN   TestOrdersRateLimit"
sleep 4
echo "--- PASS: TestOrdersRateLimit (0.41s)"
echo "PASS"
echo "ok  	shop-api/internal/api	0.418s"
EOF
chmod +x "$P/scripts/test.sh"

cat > "$P/AGENTS.md" <<'EOF'
# shop-api

Small orders API. Run `sh scripts/test.sh` before finishing a change.
EOF

cp -r "$P"/. "$W/"

cat > "$B/home/.termixgo/config.json" <<EOF
{
  "defaultModel": "deepseek-v4-pro",
  "approvalMode": "ask",
  "language": "en",
  "baseUrls": {"deepseek": "http://127.0.0.1:18700/v1"},
  "trustedFolders": ["$P"]
}
EOF

cat > "$B/home/.termixgo/secrets.json" <<'EOF'
{
  "oauth:claude-oauth": "{\"access\":\"demo-token-not-real\",\"expires\":\"2099-01-01T00:00:00Z\"}",
  "oauth:openai-codex": "{\"access\":\"demo-token-not-real\",\"accountId\":\"demo\",\"expires\":\"2099-01-01T00:00:00Z\"}"
}
EOF
chmod 600 "$B/home/.termixgo/secrets.json"
echo ok

# throwaway CA and a server certificate for the two usage hosts (demo proxy only)
cd "$B"
rm -f ca.* srv.* san.ext
openssl genrsa -out ca.key 2048 2>/dev/null
openssl req -x509 -new -key ca.key -days 3 -subj "/CN=Demo CA" -out ca.pem
openssl genrsa -out srv.key 2048 2>/dev/null
openssl req -new -key srv.key -subj "/CN=api.anthropic.com" -out srv.csr
printf "subjectAltName=DNS:api.anthropic.com,DNS:chatgpt.com\n" > san.ext
openssl x509 -req -in srv.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 3 -sha256 -extfile san.ext -out srv.pem 2>/dev/null
echo certs ok
