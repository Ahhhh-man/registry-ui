#!/usr/bin/env bash
# Pull-through cache smoke test for storage mode.
#
# An upstream registry holds three tags; a registry:3 proxy in front of it
# pulls one. registry-ui then reads the proxy's storage, read-only, and must
# show only that tag without a single request reaching the proxy or the
# upstream. A proxy answers tags/list from upstream and fetches uncached tags
# on demand, so any API call would show up in their access logs.
#
# Needs docker, curl and crane. Run it from the dev shell after loading the
# image: nix build .#image && docker load < result && .github/smoke/pull-through.sh
set -euo pipefail

image=${IMAGE:-registry-ui:latest}
registry=${REGISTRY_IMAGE:-registry:3}
p=pt-smoke-$$ # prefix for every container, network and volume
repo=smoke/app
tmp=$(mktemp -d)

cleanup() {
	docker rm -f "$p-upstream" "$p-proxy" "$p-ui" >/dev/null 2>&1 || true
	docker volume rm "$p" >/dev/null 2>&1 || true
	docker network rm "$p" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	echo "--- registry-ui logs" >&2
	docker logs "$p-ui" 2>&1 | tail -20 >&2 || true
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

# requests counts the access-log lines a registry has written so far.
requests() { docker logs "$1" 2>&1 | grep -c 'HTTP/1\.1"' || true; }

docker network create "$p" >/dev/null
docker volume create "$p" >/dev/null

echo "== upstream with tags t-cached, t-upstream-a, t-upstream-b"
docker run -d --name "$p-upstream" --network "$p" -p 127.0.0.1::5000 "$registry" >/dev/null
up=$(port "$p-upstream" 5000)
for t in t-cached t-upstream-a t-upstream-b; do
	echo "$t" >"$tmp/$t"
	tar -cf "$tmp/$t.tar" -C "$tmp" "$t"
	crane append --insecure -f "$tmp/$t.tar" -t "$up/$repo:$t" >/dev/null
	crane mutate --insecure --set-platform linux/amd64 -t "$up/$repo:$t" "$up/$repo:$t" >/dev/null
done

echo "== proxy, pulling t-cached through it"
docker run -d --name "$p-proxy" --network "$p" -p 127.0.0.1::5000 \
	-v "$p:/var/lib/registry" \
	-e REGISTRY_PROXY_REMOTEURL="http://$p-upstream:5000" \
	"$registry" >/dev/null
px=$(port "$p-proxy" 5000)
crane pull --insecure "$px/$repo:t-cached" "$tmp/pulled.tar"
cached=$(docker exec "$p-proxy" ls /var/lib/registry/docker/registry/v2/repositories/$repo/_manifests/tags)
[ "$cached" = t-cached ] || fail "the proxy should have cached only t-cached, has: $cached"

echo "== registry-ui in storage mode"
# The registry URL points at the proxy, so if storage mode ever fell back to
# the API, browsing would reach the proxy and, through it, the upstream.
docker run -d --name "$p-ui" --network "$p" -p 127.0.0.1::8080 \
	-v "$p:/var/lib/registry:ro" \
	-e REGISTRY_UI_STORAGE_PATH=/var/lib/registry \
	-e REGISTRY_UI_REGISTRY_URL="http://$p-proxy:5000" \
	"$image" >/dev/null
ui=$(port "$p-ui" 8080)

up_before=$(requests "$p-upstream")
px_before=$(requests "$p-proxy")

# page fetches a UI path, checks its status, and leaves the body in $tmp/body.
page() {
	local path=$1 want=$2 code
	code=$(curl -s -o "$tmp/body" -w '%{http_code}' "http://$ui/$path")
	[ "$code" = "$want" ] || fail "/$path answered $code, want $want"
	echo "ok   /$path $code"
}

echo "== browsing the UI"
page "" 200
grep -q "$repo" "$tmp/body" || fail "the catalog doesn't list $repo"
page "$repo" 200
grep -q t-cached "$tmp/body" || fail "the repo page doesn't list t-cached"
! grep -q t-upstream "$tmp/body" || fail "the repo page lists a tag that was never pulled"
page "$repo:t-cached" 200
page "$repo:t-upstream-a" 404

sleep 1 # let the registries flush their logs
up_after=$(requests "$p-upstream")
px_after=$(requests "$p-proxy")
echo "upstream requests: $up_before before browsing, $up_after after"
echo "proxy requests:    $px_before before browsing, $px_after after"
[ "$up_after" = "$up_before" ] || fail "browsing the UI reached the upstream"
[ "$px_after" = "$px_before" ] || fail "browsing the UI reached the proxy"
echo "PASS: storage mode showed the cache without contacting the proxy or the upstream"
