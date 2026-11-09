#!/usr/bin/env bash
# API-mode smoke test against a real registry:3, in the two setups people run:
# anonymous, and htpasswd basic auth.
#
# A multi-arch image (linux/amd64 and linux/arm64) plus an alias tag is
# pushed to an anonymous registry. registry-ui must show it on the catalog,
# repo, tag and digest pages with no sign-in UI anywhere. Another repo has
# its only tag deleted, and must show as empty rather than broken. A second
# registry serves the same storage behind htpasswd; there registry-ui must
# send visitors to sign in, reject wrong credentials, show the pages once
# signed in, and forget the session after logout.
#
# Needs docker, curl and crane. Run it from the dev shell after loading the
# image: nix build .#image && docker load < result && .github/smoke/registry.sh
# BASE_PATH=/registry/ runs the same checks with the UI under a base path.
set -euo pipefail

image=${IMAGE:-registry-ui:latest}
registry=${REGISTRY_IMAGE:-registry:3}
base=${BASE_PATH:-/}
p=reg-smoke-$$ # prefix for every container, network and volume
repo=smoke/app
empty=smoke/empty # its only tag is deleted
tmp=$(mktemp -d)
ui= # host:port of the registry-ui under test
uiname=

# The test user is smoke / smoke-secret; the line comes from
# htpasswd -Bbn smoke smoke-secret.
user=smoke
pass=smoke-secret
# shellcheck disable=SC2016 # the $s are part of the bcrypt hash
htpasswd='smoke:$2y$05$2aoRgJIfEq7vyJpiplJPrezXEUPmeSAvE2NKb/NvFFRilby0gMBDK'

cleanup() {
	docker rm -f "$p-anon" "$p-auth" "$p-ui-anon" "$p-ui-auth" >/dev/null 2>&1 || true
	docker volume rm "$p" >/dev/null 2>&1 || true
	docker network rm "$p" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	if [ -n "$uiname" ]; then
		echo "--- registry-ui logs" >&2
		docker logs "$uiname" 2>&1 | tail -20 >&2 || true
	fi
	exit 1
}

# port prints host:port for a container's published port, waiting until it answers.
port() {
	local addr
	addr=$(docker port "$1" "$2" | head -1)
	for _ in $(seq 50); do
		curl -so /dev/null "http://$addr/" && break
		sleep 0.2
	done
	echo "$addr"
}

# start_ui runs registry-ui against a registry container and sets ui and uiname.
start_ui() {
	uiname=$p-ui-$1
	docker run -d --name "$uiname" --network "$p" -p 127.0.0.1::8080 \
		-e REGISTRY_UI_REGISTRY_URL="http://$p-$1:5000" \
		-e REGISTRY_UI_BASE_PATH="$base" \
		"$image" >/dev/null
	ui=$(port "$uiname" 8080)
}

# req sends a request to a UI path (relative to the base path) with the
# cookie jar, checks the status, and leaves headers in $tmp/head and the body
# in $tmp/body. Extra arguments go to curl.
req() {
	local path=$1 want=$2 code
	shift 2
	code=$(curl -s -b "$tmp/jar" -c "$tmp/jar" -D "$tmp/head" -o "$tmp/body" -w '%{http_code}' "$@" "http://$ui$base$path")
	[ "$code" = "$want" ] || fail "$base$path answered $code, want $want"
}

# page is req for a plain GET, logged.
page() {
	req "$@"
	echo "ok   GET $base$1 $2"
}

has() { grep -qF -- "$1" "$tmp/body" || fail "$base$2 doesn't contain \"$1\""; }
lacks() { ! grep -qF -- "$1" "$tmp/body" || fail "$base$2 contains \"$1\""; }
header() { tr -d '\r' <"$tmp/head" | grep -qi "^$1" || fail "no \"$1\" header on $base$2"; }

# post_login signs in with a password and checks the status.
post_login() {
	req -/login "$2" -H "Origin: http://$ui" \
		--data-urlencode "username=$user" --data-urlencode "password=$1" --data-urlencode "next=$base"
	echo "ok   POST $base-/login ($3) $2"
}

docker network create "$p" >/dev/null
docker volume create "$p" >/dev/null

echo "== anonymous registry, pushing $repo:amd64 and :arm64, index :v1 over both, alias :latest"
docker run -d --name "$p-anon" --network "$p" -p 127.0.0.1::5000 -v "$p:/var/lib/registry" \
	-e REGISTRY_STORAGE_DELETE_ENABLED=true "$registry" >/dev/null
reg=$(port "$p-anon" 5000)
for arch in amd64 arm64; do
	echo "$arch" >"$tmp/arch"
	tar -cf "$tmp/layer.tar" -C "$tmp" arch
	crane append --insecure -f "$tmp/layer.tar" -t "$reg/$repo:$arch" >/dev/null
	crane mutate --insecure --set-platform "linux/$arch" -t "$reg/$repo:$arch" "$reg/$repo:$arch" >/dev/null
done
crane index append --insecure -m "$reg/$repo:amd64" -m "$reg/$repo:arm64" -t "$reg/$repo:v1" >/dev/null
crane tag --insecure "$reg/$repo:v1" latest
digest=$(crane digest --insecure "$reg/$repo:v1")
amd64=$(crane digest --insecure "$reg/$repo:amd64")

echo "== pushing $empty:v1, then deleting it"
crane copy --insecure "$reg/$repo:amd64" "$reg/$empty:v1" >/dev/null
crane delete --insecure "$reg/$empty@$amd64"

echo "== registry-ui on the anonymous registry"
start_ui anon
# anon_page is page plus a check that an open registry shows no sign-in UI.
anon_page() {
	page "$1" 200
	lacks "Sign in" "$1"
	lacks "-/login" "$1"
}
anon_page ""
has "$repo" ""
anon_page "$repo"
has v1 "$repo"
has latest "$repo"
anon_page "$repo:v1"
has linux/amd64 "$repo:v1"
has linux/arm64 "$repo:v1"
anon_page "$repo@$digest"
has linux/arm64 "$repo@$digest"
anon_page "$repo@$amd64"
has linux/amd64 "$repo@$amd64"
page "$repo:nope" 404
# Distribution keeps the repo in the catalog with no tags.
req "?fragment=row&repo=$empty" 200 -H "HX-Request: true"
has "no tags</span>" "?fragment=row&repo=$empty"
has ">0 tags</span>" "?fragment=row&repo=$empty"
lacks "Couldn't load" "?fragment=row&repo=$empty"
echo "ok   GET $base?fragment=row&repo=$empty 200, empty"
anon_page "$empty"
has "Every tag in this repository has been deleted." "$empty"

echo "== htpasswd registry on the same storage"
printf '%s\n' "$htpasswd" >"$tmp/htpasswd"
chmod 755 "$tmp"
chmod 644 "$tmp/htpasswd"
docker run -d --name "$p-auth" --network "$p" -p 127.0.0.1::5000 -v "$p:/var/lib/registry" \
	-v "$tmp/htpasswd:/auth/htpasswd:ro" \
	-e REGISTRY_AUTH=htpasswd \
	-e REGISTRY_AUTH_HTPASSWD_REALM=smoke \
	-e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd \
	"$registry" >/dev/null
port "$p-auth" 5000 >/dev/null # wait until it answers

echo "== registry-ui on the htpasswd registry"
start_ui auth
next=${base//\//%2F} # the base path, query-escaped
login="${base}-/login?next=$next"

page "" 303
header "location: $login\$" ""
req "" 401 -H "HX-Request: true"
header "hx-redirect: $login\$" ""
echo "ok   GET $base (htmx) 401 with HX-Redirect"

page "-/login?next=$next" 200
has 'name="password"' "-/login"
post_login wrong 401 "wrong password"
has "The registry rejected these credentials." -/login
post_login "$pass" 303 "right password"
header "location: $base\$" -/login
header "set-cookie: session=[^;]*; Path=$base;.*HttpOnly.*SameSite=Strict" -/login

for path in "" "$repo" "$repo:v1" "$repo@$digest"; do
	page "$path" 200
	has "Signed in as $user" "$path"
done
has linux/arm64 "$repo@$digest"

req -/logout 303 -X POST -H "Origin: http://$ui"
echo "ok   POST $base-/logout 303"
page "" 303
header "location: $login\$" ""
page "$repo:v1" 303
echo "PASS: registry-ui works against registry:3, anonymous and with htpasswd (base path $base)"
