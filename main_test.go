package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestHealthcheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	load := func(listen, base string) config.Config {
		cfg, err := config.Load([]string{
			// Nothing listens on port 1: the healthcheck must not care.
			"REGISTRY_UI_REGISTRY_URL=http://127.0.0.1:1",
			"REGISTRY_UI_LISTEN=" + listen,
			"REGISTRY_UI_BASE_PATH=" + base,
		})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	cfg := load(":"+port, "/registry")
	srv := &http.Server{Handler: web.New(cfg.BasePath, web.Handlers{Static: ui.StaticHandler()})}
	go func() { _ = srv.Serve(ln) }()

	for _, listen := range []string{":" + port, "0.0.0.0:" + port, "[::]:" + port, "127.0.0.1:" + port} {
		if err := healthcheck(load(listen, "/registry")); err != nil {
			t.Errorf("listen %q: %v", listen, err)
		}
	}
	if err := healthcheck(load(":"+port, "/")); err == nil {
		t.Error("wrong base path: want error")
	}

	_ = srv.Close()
	if err := healthcheck(cfg); err == nil {
		t.Error("after shutdown: want error")
	}
}

// purgeNow is after every seeded image: Index is 42 days old, Single 611.
var purgeNow = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

type purgeRun struct {
	code        int
	out, errOut string
}

// runPurge runs purge against url at now, with extra environment variables
// and stdin.
func runPurge(t *testing.T, url string, now time.Time, env []string, stdin string, args ...string) purgeRun {
	t.Helper()
	var out, errOut bytes.Buffer
	env = append([]string{"REGISTRY_UI_REGISTRY_URL=" + url}, env...)
	code := purge(t.Context(), args, env, strings.NewReader(stdin), &out, &errOut, now)
	return purgeRun{code, out.String(), errOut.String()}
}

// wantLines fails unless every line is in out.
func (r purgeRun) wantLines(t *testing.T, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !slices.Contains(strings.Split(r.out, "\n"), l) {
			t.Errorf("missing line %q in:\n%s", l, r.out)
		}
	}
}

// allTags lists every repository's tags, as "repo:tag", sorted.
func allTags(ctx context.Context, t *testing.T, url string) []string {
	t.Helper()
	api, err := registry.NewAPI(url, false)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := api.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, repo := range repos {
		tags, err := api.Tags(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range tags {
			all = append(all, repo+":"+tag)
		}
	}
	slices.Sort(all)
	return all
}

// The dry run lists every tag with the rule that kept or removed it, and
// deletes nothing.
func TestPurgeDryRun(t *testing.T) {
	r := registrytest.New(t)
	before := allTags(t.Context(), t, r.URL)
	run := runPurge(t, r.URL, purgeNow, nil, "", "--keep-days", "60")
	if run.code != 0 || run.errOut != "" {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	if after := allTags(t.Context(), t, r.URL); !slices.Equal(after, before) {
		t.Errorf("tags changed in a dry run: %v", after)
	}
	for _, tag := range before {
		if !strings.Contains(run.out, " "+tag+"  (") {
			t.Errorf("%s not listed", tag)
		}
	}
	run.wantLines(t,
		"Dry run: nothing will be deleted. Run again with --apply to delete what's marked delete.",
		"keep   library/nginx:1.0.0  (created 2024-11-20, within 60 days)",
		"keep   library/nginx:"+r.SignatureTag+"  (signature, deleted only with its image)",
		"delete library/busybox:1.36  (created 2023-05-01, older than 60 days)",
		"keep   test/undated:bad-label  (unknown created date)",
		"keep   charts/podinfo:6.7.1  (unknown created date)",
		"keep   missing/all:1.0.0  (unknown created date)",
		"keep   missing/manifest:latest  (unknown created date)",
		"delete test/paged:v10  (created 2023-05-01, older than 60 days)",
		"Dry run: would delete 17 tags (8 images). Nothing was deleted; add --apply to delete them.",
	)
}

// --keep-count and --keep-days keep the union: whichever keeps more. Equal
// dates rank by name.
func TestPurgeKeepUnion(t *testing.T) {
	r := registrytest.New(t)
	now := registrytest.SingleCreated.AddDate(0, 0, 10) // dated is 10 days old
	run := runPurge(t, r.URL, now, nil, "", "--repos", registrytest.RepoUndated, "--keep-count", "1", "--keep-days", "500")
	run.wantLines(t,
		"keep   test/undated:dated  (created 2023-05-01, within 500 days)",
		"keep   test/undated:labelled  (created 2022-03-04, within 500 days)",
		"delete test/undated:annotated  (created 2021-06-07, older than 500 days, not among the 1 newest)",
		"delete test/undated:index-annotated  (created 2021-06-07, older than 500 days, not among the 1 newest)",
	)
	run = runPurge(t, r.URL, now, nil, "", "--repos", registrytest.RepoUndated, "--keep-count", "3", "--keep-days", "30")
	run.wantLines(t,
		"keep   test/undated:dated  (created 2023-05-01, within 30 days)",
		"keep   test/undated:labelled  (created 2022-03-04, among the 3 newest)",
		"keep   test/undated:annotated  (created 2021-06-07, among the 3 newest)",
		"delete test/undated:index-annotated  (created 2021-06-07, older than 30 days, not among the 3 newest)",
		"keep   test/undated:bad-label  (unknown created date)",
	)
}

// A digest is deleted only if every tag on it would be: one kept tag keeps
// the image, and its signature.
func TestPurgeSharedDigestSurvives(t *testing.T) {
	r := registrytest.New(t)
	before := allTags(t.Context(), t, r.URL)
	run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", registrytest.RepoIndex, "--keep-tags", "^latest$", "--apply")
	if run.code != 0 {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	run.wantLines(t,
		"keep   library/nginx:latest  (matches --keep-tags)",
		"keep   library/nginx:1.0.0  (shares its image with kept tag latest)",
		"keep   library/nginx:"+r.SignatureTag+"  (signature, deleted only with its image)",
		"Deleted 0 tags (0 images); 0 failed.",
	)
	if after := allTags(t.Context(), t, r.URL); !slices.Equal(after, before) {
		t.Errorf("tags = %v", after)
	}

	// v1 to v10 are one image: keeping the newest by name keeps them all.
	run = runPurge(t, r.URL, purgeNow, nil, "", "--repos", registrytest.RepoPaged, "--keep-count", "1")
	run.wantLines(t,
		"keep   test/paged:v1  (created 2023-05-01, among the 1 newest)",
		"keep   test/paged:v10  (shares its image with kept tag v1)",
		"Dry run: would delete 0 tags (0 images). Nothing was deleted; add --apply to delete them.",
	)
}

// --apply deletes, and an image's signature goes with it; nothing outside
// --repos is touched.
func TestPurgeApply(t *testing.T) {
	r := registrytest.New(t)
	run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", "library/*", "--keep-days", "30", "--apply")
	if run.code != 0 || run.errOut != "" {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	run.wantLines(t,
		"delete library/nginx:1.0.0  (created 2024-11-20, older than 30 days)",
		"delete library/nginx:"+r.SignatureTag+"  (signature of a deleted image)",
		"delete library/busybox:1.36  (created 2023-05-01, older than 30 days)",
		"Deleted 4 tags (2 images); 0 failed.",
	)
	for _, tag := range allTags(t.Context(), t, r.URL) {
		if strings.HasPrefix(tag, "library/") {
			t.Errorf("%s left", tag)
		}
	}
	if tags := allTags(t.Context(), t, r.URL); !slices.Contains(tags, "test/paged:v1") {
		t.Errorf("out of scope repository touched: %v", tags)
	}

	// Unknown dates are kept, however old the rest.
	run = runPurge(t, r.URL, purgeNow, nil, "", "--repos", registrytest.RepoUndated, "--keep-tags", "^dated$", "--apply")
	if run.code != 0 {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	var undated []string
	for _, tag := range allTags(t.Context(), t, r.URL) {
		if strings.HasPrefix(tag, registrytest.RepoUndated+":") {
			undated = append(undated, tag)
		}
	}
	if want := []string{"test/undated:bad-label", "test/undated:dated"}; !slices.Equal(undated, want) {
		t.Errorf("left %v, want %v", undated, want)
	}
}

// A signature manifest that also carries an ordinary tag stays when its
// image goes, or deleting it would take that tag too.
func TestPurgeSignatureWithOrdinaryTag(t *testing.T) {
	r := registrytest.New(t)
	ref, err := name.ParseReference(r.Host+"/"+registrytest.RepoIndex+":copy", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, r.Signature); err != nil {
		t.Fatal(err)
	}
	run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", registrytest.RepoIndex, "--keep-days", "30", "--apply")
	if run.code != 0 {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	run.wantLines(t,
		"keep   library/nginx:copy  (unknown created date)",
		"keep   library/nginx:"+r.SignatureTag+"  (signature, shares its manifest with tag copy)",
		"Deleted 2 tags (1 image); 0 failed.",
	)
	if tags := allTags(t.Context(), t, r.URL); !slices.Contains(tags, "library/nginx:copy") || !slices.Contains(tags, "library/nginx:"+r.SignatureTag) {
		t.Errorf("tags = %v", tags)
	}
}

// A failed delete is reported and the rest still go, then purge exits 1.
func TestPurgePartialFailure(t *testing.T) {
	r := registrytest.New(t)
	d, err := r.Single.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.RefuseDelete(registrytest.RepoSingle, d.String(), http.StatusInternalServerError)
	run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", "library/*", "--keep-days", "30", "--apply")
	if run.code != 1 || !strings.Contains(run.errOut, "deleting library/busybox@"+d.String()) {
		t.Errorf("code %d, stderr %q", run.code, run.errOut)
	}
	run.wantLines(t, "delete library/nginx:1.0.0  (created 2024-11-20, older than 30 days)", "Deleted 3 tags (1 image); 1 failed.")
	if !strings.Contains(run.out, "failed library/busybox:1.36  (") {
		t.Errorf("failure not listed:\n%s", run.out)
	}
	if tags := allTags(t.Context(), t, r.URL); !slices.Contains(tags, "library/busybox:1.36") || slices.Contains(tags, "library/nginx:latest") {
		t.Errorf("tags = %v", tags)
	}
}

// A repository whose tags can't be read is reported and skipped; the rest
// still go, then purge exits 1.
func TestPurgeUnreadableRepo(t *testing.T) {
	r := registrytest.New(t)
	target, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/"+registrytest.RepoSingle+"/tags/list" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	run := runPurge(t, srv.URL, purgeNow, nil, "", "--repos", "library/*", "--keep-days", "30", "--apply")
	if run.code != 1 || !strings.Contains(run.errOut, "purge: library/busybox: ") || !strings.Contains(run.errOut, "couldn't read 1 of 2 repositories") {
		t.Errorf("code %d, stderr %q", run.code, run.errOut)
	}
	run.wantLines(t, "Deleted 3 tags (1 image); 0 failed.")
	if tags := allTags(t.Context(), t, r.URL); !slices.Contains(tags, "library/busybox:1.36") || slices.Contains(tags, "library/nginx:latest") {
		t.Errorf("tags = %v", tags)
	}
}

// A registry that refuses deletes, or this identity, stops purge before it
// deletes or plans anything, in a dry run too.
func TestPurgeCannotDelete(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   string
	}{
		"disabled":  {http.StatusMethodNotAllowed, "REGISTRY_STORAGE_DELETE_ENABLED"},
		"forbidden": {http.StatusForbidden, "anonymous access may not delete"},
	} {
		t.Run(name, func(t *testing.T) {
			r := registrytest.New(t)
			r.RefuseDelete("", "", tc.status)
			before := allTags(t.Context(), t, r.URL)
			for _, apply := range []string{"--apply=false", "--apply"} {
				run := runPurge(t, r.URL, purgeNow, nil, "", "--keep-days", "1", apply)
				if run.code != 1 || run.out != "" || !strings.Contains(run.errOut, tc.want) {
					t.Errorf("%s: code %d, stdout %q, stderr %q", apply, run.code, run.out, run.errOut)
				}
			}
			if after := allTags(t.Context(), t, r.URL); !slices.Equal(after, before) {
				t.Errorf("tags = %v", after)
			}
		})
	}
	r := registrytest.NewDeleteDisabled(t)
	if run := runPurge(t, r.URL, purgeNow, nil, "", "--keep-days", "1"); run.code != 1 {
		t.Errorf("NewDeleteDisabled: code %d", run.code)
	}
}

// Storage mode can't delete, so purge refuses before contacting anything.
func TestPurgeStorageMode(t *testing.T) {
	run := runPurge(t, "http://127.0.0.1:1", purgeNow, []string{"REGISTRY_UI_STORAGE_PATH=" + t.TempDir()}, "", "--keep-days", "1")
	if run.code != 1 || run.out != "" || !strings.Contains(run.errOut, "REGISTRY_UI_STORAGE_PATH") {
		t.Errorf("code %d, stdout %q, stderr %q", run.code, run.out, run.errOut)
	}
}

// Purge signs in only as --user, with the password from the environment
// or else stdin; without --user it's anonymous.
func TestPurgeCredentials(t *testing.T) {
	r := registrytest.NewWithBasicAuth(t, "alice", "s3cret")
	for name, tc := range map[string]struct {
		env   []string
		stdin string
		args  []string
		code  int
		want  string
	}{
		"env":            {env: []string{"REGISTRY_UI_PURGE_PASSWORD=s3cret"}, stdin: "wrong\n", args: []string{"--user", "alice"}},
		"stdin":          {stdin: "s3cret\nmore\n", args: []string{"--user", "alice"}},
		"stdin no eol":   {stdin: "s3cret", args: []string{"--user", "alice"}},
		"wrong password": {stdin: "nope\n", args: []string{"--user", "alice"}, code: 1, want: "unauthorized"},
		"no password":    {args: []string{"--user", "alice"}, code: 1, want: "--user needs a password"},
		"anonymous":      {env: []string{"REGISTRY_UI_PURGE_PASSWORD=s3cret"}, code: 1, want: "runs anonymously"},
	} {
		t.Run(name, func(t *testing.T) {
			run := runPurge(t, r.URL, purgeNow, tc.env, tc.stdin, append(tc.args, "--keep-days", "1")...)
			if run.code != tc.code || !strings.Contains(run.errOut, tc.want) {
				t.Errorf("code %d, stderr %q", run.code, run.errOut)
			}
		})
	}
}

// --repos and --exclude-repos match whole names, and * stops at a slash.
func TestPurgeRepoGlobs(t *testing.T) {
	r := registrytest.New(t)
	run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", "library/*, test/*,team/*", "--exclude-repos", "test/paged", "--keep-days", "1")
	if run.code != 0 {
		t.Fatalf("code %d, stderr %q", run.code, run.errOut)
	}
	var repos []string
	for line := range strings.Lines(run.out) {
		if f := strings.Fields(line); len(f) > 1 && (f[0] == "keep" || f[0] == "delete") {
			if repo, _, _ := strings.Cut(f[1], ":"); !slices.Contains(repos, repo) {
				repos = append(repos, repo)
			}
		}
	}
	if want := []string{"library/busybox", "library/nginx", "test/undated"}; !slices.Equal(repos, want) {
		t.Errorf("repos = %v, want %v", repos, want)
	}
	if run := runPurge(t, r.URL, purgeNow, nil, "", "--repos", "nothing/*", "--keep-days", "1"); run.code != 1 {
		t.Errorf("no match: code %d", run.code)
	}
}

func TestPurgeUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--apply"},
		{"--keep-days", "0"},
		{"--keep-days", "-1"},
		{"--keep-count", "x"},
		{"--keep-tags", "("},
		{"--keep-days", "1", "--repos", "["},
		{"--keep-days", "1", "--exclude-repos", "a,["},
		{"--keep-days", "1", "--bogus"},
		{"--keep-days", "1", "extra"},
	} {
		// Nothing listens on port 1: usage errors come first.
		if run := runPurge(t, "http://127.0.0.1:1", purgeNow, nil, "", args...); run.code != 2 || run.errOut == "" {
			t.Errorf("%q: code %d, stderr %q", args, run.code, run.errOut)
		}
	}
}
