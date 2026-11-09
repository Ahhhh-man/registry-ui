#!/usr/bin/env bash
# Pull-through cache smoke test for storage mode.
#
# An upstream registry holds a few tags, one a two-platform image; a
# registry:3 proxy in front of it pulls two tags a couple of seconds apart,
# and one platform of the two-platform image. registry-ui then reads the
# proxy's storage, read-only, and must show only those tags, the later pull
# first when sorted by "Recently updated" and the other platform not cached,
# without a single request reaching the proxy or the upstream. A proxy answers tags/list from upstream and fetches uncached tags
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

echo "== upstream with tags t-cached, t-again, t-upstream and t-arch (amd64 and arm64)"
docker run -d --name "$p-upstream" --network "$p" -p 127.0.0.1::5000 "$registry" >/dev/null
up=$(port "$p-upstream" 5000)
for t in t-cached t-again t-upstream t-upstream-arm64; do
	echo "$t" >"$tmp/$t"
	tar -cf "$tmp/$t.tar" -C "$tmp" "$t"
	crane append --insecure -f "$tmp/$t.tar" -t "$up/$repo:$t" >/dev/null
	platform=linux/amd64
	[ "$t" = t-upstream-arm64 ] && platform=linux/arm64
	crane mutate --insecure --set-platform "$platform" -t "$up/$repo:$t" "$up/$repo:$t" >/dev/null
done
crane index append --insecure -m "$up/$repo:t-upstream" -m "$up/$repo:t-upstream-arm64" -t "$up/$repo:t-arch" >/dev/null

echo "== proxy, pulling t-cached and the arm64 half of t-arch, then t-again, through it"
docker run -d --name "$p-proxy" --network "$p" -p 127.0.0.1::5000 \
	-v "$p:/var/lib/registry" \
	-e REGISTRY_PROXY_REMOTEURL="http://$p-upstream:5000" \
	"$registry" >/dev/null
px=$(port "$p-proxy" 5000)
crane pull --insecure "$px/$repo:t-cached" "$tmp/pulled.tar"
crane pull --insecure --platform linux/arm64 "$px/$repo:t-arch" "$tmp/arch.tar"
sleep 2 # the proxy writes each tag's link as it's pulled; the UI sorts by its mtime
crane pull --insecure "$px/$repo:t-again" "$tmp/pulled.tar"
cached=$(docker exec "$p-proxy" ls /var/lib/registry/docker/registry/v2/repositories/$repo/_manifests/tags | xargs)
[ "$cached" = "t-again t-arch t-cached" ] || fail "the proxy should have cached only t-again, t-arch and t-cached, has: $cached"

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
# firsttag prints the first tag card's tag in $tmp/body.
firsttag() { grep -o "\"/$repo:[^\"]*\" title=" "$tmp/body" | head -1 | sed 's/.*://; s/" title=//'; }
page "$repo?tab=tags" 200
[ "$(firsttag)" = t-cached ] || fail "version order should put t-cached first, got $(firsttag)"
page "$repo?sort=updated" 200
[ "$(firsttag)" = t-again ] || fail "\"Recently updated\" should put t-again, pulled last, first; got $(firsttag)"
page "$repo:t-cached" 200
grep -q "Updated today" "$tmp/body" || fail "the tag page doesn't say when t-cached was updated"
page "$repo:t-arch" 200
grep -q '1 platform (1 missing)' "$tmp/body" || fail "t-arch's page doesn't count linux/amd64 as missing"
grep -q 'id="platform-linux-arm64" class="[^"]*bg-surface' "$tmp/body" || fail "t-arch's page doesn't show linux/arm64 as cached"
grep -q 'id="platform-linux-amd64" class="[^"]*border-dashed' "$tmp/body" || fail "t-arch's page doesn't dim linux/amd64"
[ "$(grep -c 'Not cached. Nobody has pulled this platform' "$tmp/body")" = 1 ] || fail "t-arch's page should show one platform as not cached"
page "$repo:t-upstream" 404

sleep 1 # let the registries flush their logs
up_after=$(requests "$p-upstream")
px_after=$(requests "$p-proxy")
echo "upstream requests: $up_before before browsing, $up_after after"
echo "proxy requests:    $px_before before browsing, $px_after after"
[ "$up_after" = "$up_before" ] || fail "browsing the UI reached the upstream"
[ "$px_after" = "$px_before" ] || fail "browsing the UI reached the proxy"
echo "PASS: storage mode showed the cache without contacting the proxy or the upstream"
