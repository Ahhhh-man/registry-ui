// Command registry-ui serves a web UI for a CNCF Distribution registry.
//
// Usage:
//
//	registry-ui [serve]       run the web server (default)
//	registry-ui healthcheck   exit 0 if the server is healthy
//	registry-ui purge [flags] delete old tags; only a dry run without --apply
//
// Purge never starts the server. It reads the registry settings from the
// same REGISTRY_UI_* variables, but signs in only as --user, with the
// password from REGISTRY_UI_PURGE_PASSWORD or stdin, never with anything
// the server holds. Run registry-ui purge -h for its flags.
//
// Built with -tags fake, it serves built-in sample data instead of a real
// registry, which is handy for UI work. REGISTRY_UI_REGISTRY_URL must still
// be set, but is not used.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/format"
	"github.com/Ahhhh-man/registry-ui/internal/pages"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/session"
	"github.com/Ahhhh-man/registry-ui/internal/ui"
	"github.com/Ahhhh-man/registry-ui/internal/web"
	"golang.org/x/sync/errgroup"
)

var version = "dev"

// memoryLimit is the Go runtime's soft memory limit, under the 15 MB idle
// budget with room for what the runtime doesn't count.
const memoryLimit = 10 << 20

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case "healthcheck":
		cfg, err := config.Load(os.Environ())
		if err == nil {
			err = healthcheck(cfg)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
			os.Exit(1)
		}
	case "purge":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := purge(ctx, os.Args[2:], os.Environ(), os.Stdin, os.Stdout, os.Stderr, time.Now())
		stop()
		os.Exit(code)
	default:
		fmt.Fprintf(os.Stderr, "usage: %s [serve|healthcheck|purge]\n", os.Args[0])
		os.Exit(2)
	}
}

func serve() error {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration:\n%v\n", err)
		os.Exit(1)
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)
	slog.Info("starting registry-ui", "version", version, "config", cfg)
	// Collect sooner, and hand memory back to the OS, rather than let the
	// heap double past the cache. GOMEMLIMIT overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}
	src, err := newSource(cfg)
	if err != nil {
		return err
	}
	sessions, err := session.New(cfg.Secret, cfg.BasePath)
	if err != nil {
		return err
	}
	// Login checks credentials with the source itself, never the cache.
	check, _ := src.(interface{ Check(context.Context) error })
	challenged, _ := src.(interface{ Challenged() bool })
	src = registry.NewCache(src)

	funcs := web.FuncMap(cfg.BasePath, ui.StaticName)
	maps.Copy(funcs, format.Funcs())
	tmpl, err := ui.Templates(funcs)
	if err != nil {
		return err
	}
	p := &pages.Server{Src: src, Templates: tmpl, Title: cfg.Title, Version: version, PullHost: cfg.PullURL, BasePath: cfg.BasePath, Sessions: sessions, Storage: cfg.StoragePath != "", TagSort: cfg.TagSort, TagsPerPage: cfg.TagsPerPage, CatalogSort: cfg.CatalogSort, Delete: cfg.EnableDelete && cfg.StoragePath == ""}
	if check != nil {
		p.Check = check.Check
	}
	if challenged != nil {
		p.Challenged = challenged.Challenged
	}
	handlers := p.Handlers()
	handlers.Static = ui.StaticHandler()
	h := web.New(cfg.BasePath, handlers)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Middleware(h, ui.ErrorRenderer(tmpl, p.Header)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // above the 10 s upstream timeout, with room for a few calls
		IdleTimeout:       2 * time.Minute,
	}
	return srv.ListenAndServe()
}

// newSource builds the Source the pages read from. In builds tagged fake,
// fake.go swaps in sample data.
var newSource = func(cfg config.Config) (registry.Source, error) {
	if cfg.StoragePath != "" {
		slog.Warn("storage mode: the registry's auth is not consulted, so anyone who can reach this UI can see every repository; put it behind your own auth if that matters")
		if len(cfg.RegistryHeaders) > 0 {
			slog.Warn("storage mode: REGISTRY_UI_REGISTRY_HEADER_* is ignored, since the registry's API isn't used")
		}
		if cfg.RegistryClientCert != nil {
			slog.Warn("storage mode: REGISTRY_UI_REGISTRY_CLIENT_CERT and REGISTRY_UI_REGISTRY_CLIENT_KEY are ignored, since the registry's API isn't used")
		}
		if cfg.EnableDelete {
			slog.Warn("storage mode: REGISTRY_UI_ENABLE_DELETE is ignored, since storage mode only reads")
		}
		return registry.NewStorage(cfg.StoragePath)
	}
	api, err := newAPI(cfg)
	if err != nil {
		return nil, err
	}
	return api, nil
}

// newAPI connects to the registry's API as cfg says, for the server and
// for purge.
func newAPI(cfg config.Config) (*registry.API, error) {
	return registry.NewAPI(cfg.RegistryURL.String(), cfg.RegistryInsecure, registry.WithHeader(cfg.RegistryHeaders), registry.WithClientCert(cfg.RegistryClientCert))
}

// healthcheck fetches one embedded static asset from the UI's own listener.
// It never talks to the registry, so a registry outage doesn't restart the UI.
func healthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + cfg.BasePath + "-/static/" + ui.StaticName("htmx.min.js")
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}

// purgeLimit caps the tags purge reads at once in a repository.
const purgeLimit = 8

// purgeRules are what purge keeps, from its flags.
type purgeRules struct {
	days, count int
	tags        *regexp.Regexp
}

// verdict is what purge does with one tag, and why.
type verdict struct {
	verb, why string
}

// purge runs `registry-ui purge`: it deletes the tags of every repository
// in scope that no keep rule keeps, or with no --apply says what it would
// delete. It returns the exit code: 2 for bad usage, 1 if anything failed.
func purge(ctx context.Context, args, environ []string, stdin io.Reader, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "delete; without it, only say what would be deleted")
	var rules purgeRules
	fs.IntVar(&rules.days, "keep-days", 0, "keep tags whose image was created within the last `N` days")
	fs.IntVar(&rules.count, "keep-count", 0, "keep the `N` newest tags in each repository")
	keepTags := fs.String("keep-tags", "", "keep tags whose name matches `regexp`")
	reposFlag := fs.String("repos", "", "only look at repositories matching one of these comma-separated `globs`")
	excludeFlag := fs.String("exclude-repos", "", "skip repositories matching one of these comma-separated `globs`")
	user := fs.String("user", "", "sign in as `name`, with the password from REGISTRY_UI_PURGE_PASSWORD or stdin")
	fs.Usage = func() {
		printf(fs.Output(), "usage: registry-ui purge [flags]\n\nDeletes old tags. Without --apply it only says what it would delete.\nGive at least one of --keep-days, --keep-count and --keep-tags.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(format string, a ...any) int {
		printf(stderr, "purge: "+format+"\n", a...)
		return 2
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument %q", fs.Arg(0))
	}
	if rules.days < 0 || rules.count < 0 {
		return usage("--keep-days and --keep-count can't be negative")
	}
	if *keepTags != "" {
		re, err := regexp.Compile(*keepTags)
		if err != nil {
			return usage("--keep-tags: %v", err)
		}
		rules.tags = re
	}
	// With no rule every dated tag would go, which is never what's meant.
	if rules.days == 0 && rules.count == 0 && rules.tags == nil {
		return usage("give at least one of --keep-days, --keep-count and --keep-tags")
	}
	include, err := globs(*reposFlag)
	if err != nil {
		return usage("--repos: %v", err)
	}
	exclude, err := globs(*excludeFlag)
	if err != nil {
		return usage("--exclude-repos: %v", err)
	}

	env := map[string]string{}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["REGISTRY_UI_STORAGE_PATH"] != "" {
		printf(stderr, "purge: REGISTRY_UI_STORAGE_PATH is set, but storage mode only reads; purge deletes through the registry's API, so unset it and set REGISTRY_UI_REGISTRY_URL\n")
		return 1
	}
	cfg, err := config.Load(environ)
	if err != nil {
		printf(stderr, "purge: invalid configuration:\n%v\n", err)
		return 1
	}

	// Only the credentials given to purge: never any the server holds.
	pass := env["REGISTRY_UI_PURGE_PASSWORD"]
	switch {
	case *user != "" && pass == "":
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			printf(stderr, "purge: reading the password from stdin: %v\n", err)
			return 1
		}
		if pass = strings.TrimRight(line, "\r\n"); pass == "" {
			printf(stderr, "purge: --user needs a password, in REGISTRY_UI_PURGE_PASSWORD or on stdin\n")
			return 1
		}
	case *user == "" && pass != "":
		printf(stderr, "purge: REGISTRY_UI_PURGE_PASSWORD is set without --user, so it's ignored and purge runs anonymously\n")
	}
	if *user != "" {
		ctx = registry.WithCredentials(ctx, *user, pass)
	}

	api, err := newAPI(cfg)
	if err != nil {
		printf(stderr, "purge: %v\n", err)
		return 1
	}
	catalog, err := api.Catalog(ctx)
	if err != nil {
		printf(stderr, "purge: listing repositories: %v\n", err)
		return 1
	}
	repos := slices.DeleteFunc(catalog, func(repo string) bool {
		return include != nil && !matchAny(include, repo) || matchAny(exclude, repo)
	})
	if len(repos) == 0 {
		if include != nil || exclude != nil {
			printf(stderr, "purge: no repository matches --repos and --exclude-repos\n")
			return 1
		}
		printf(stdout, "The registry has no repositories.\n")
		return 0
	}

	// Asked even in a dry run, so it tells you the real run would fail.
	switch err := api.CanDelete(ctx, repos[0]); {
	case errors.Is(err, registry.ErrDeleteDisabled):
		printf(stderr, "purge: the registry doesn't allow deletes. Distribution needs REGISTRY_STORAGE_DELETE_ENABLED=true, and pull-through caches never allow them. Nothing was deleted.\n")
		return 1
	case errors.Is(err, registry.ErrUnauthorized), errors.Is(err, registry.ErrForbidden):
		who := "anonymous access"
		if *user != "" {
			who = "user " + *user
		}
		printf(stderr, "purge: %s may not delete from %s (%v); pass --user with an account that may. Nothing was deleted.\n", who, repos[0], err)
		return 1
	case err != nil:
		printf(stderr, "purge: checking whether the registry allows deletes: %v. Nothing was deleted.\n", err)
		return 1
	}

	if !*apply {
		printf(stdout, "Dry run: nothing will be deleted. Run again with --apply to delete what's marked delete.\n")
	}
	var tags, images, failed, unread int
	for _, repo := range repos {
		p, err := purgeRepo(ctx, api, repo, rules, now)
		if err != nil {
			printf(stderr, "purge: %s: %v; nothing in it was deleted\n", repo, err)
			unread++
			continue
		}
		var results []registry.DeleteResult
		if *apply {
			results = p.plan.Apply(ctx, api)
		}
		t, i, f := p.report(stdout, stderr, *apply, results)
		tags, images, failed = tags+t, images+i, failed+f
	}
	if *apply {
		printf(stdout, "Deleted %s (%s); %d failed.\n", plural(tags, "tag"), plural(images, "image"), failed)
	} else {
		printf(stdout, "Dry run: would delete %s (%s). Nothing was deleted; add --apply to delete them.\n", plural(tags, "tag"), plural(images, "image"))
	}
	if unread > 0 {
		printf(stderr, "purge: couldn't read %d of %d repositories, so nothing in those was deleted\n", unread, len(repos))
	}
	if failed > 0 || unread > 0 {
		return 1
	}
	return 0
}

// repoPurge is what purge decided for one repository.
type repoPurge struct {
	repo     string
	tags     []string // sorted
	verdicts map[string]verdict
	// plan deletes only whole images: every tag on them is a candidate.
	plan registry.DeletePlan
}

// purgeRepo decides what to do with each tag of repo. A tag it can't read
// fails the whole repository, since it might have saved another.
func purgeRepo(ctx context.Context, api *registry.API, repo string, rules purgeRules, now time.Time) (repoPurge, error) {
	tags, err := api.Tags(ctx, repo)
	if err != nil {
		return repoPurge{}, err
	}
	slices.Sort(tags)
	created := make([]time.Time, len(tags))
	var g errgroup.Group
	g.SetLimit(purgeLimit)
	for i, tag := range tags {
		if registry.IsSignatureTag(tag) {
			continue
		}
		g.Go(func() error {
			var err error
			created[i], err = tagCreated(ctx, api, repo, tag)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return repoPurge{}, err
	}

	// Newest first; equal dates by name, so the order never changes.
	var dated []int
	for i, c := range created {
		if !c.IsZero() {
			dated = append(dated, i)
		}
	}
	slices.SortStableFunc(dated, func(a, b int) int { return created[b].Compare(created[a]) })
	newest := map[string]bool{}
	for _, i := range dated[:min(rules.count, len(dated))] {
		newest[tags[i]] = true
	}

	p := repoPurge{repo: repo, tags: tags, verdicts: map[string]verdict{}}
	cutoff := now.AddDate(0, 0, -rules.days)
	var candidates []string
	for i, tag := range tags {
		c := created[i]
		date := "created " + c.UTC().Format("2006-01-02")
		v := verdict{verb: "keep"}
		switch {
		case registry.IsSignatureTag(tag):
			v.why = "signature, deleted only with its image"
		case c.IsZero():
			v.why = "unknown created date"
		case rules.tags != nil && rules.tags.MatchString(tag):
			v.why = "matches --keep-tags"
		case rules.days > 0 && !c.Before(cutoff):
			v.why = fmt.Sprintf("%s, within %d days", date, rules.days)
		case newest[tag]:
			v.why = fmt.Sprintf("%s, among the %d newest", date, rules.count)
		default:
			v.verb, v.why = "delete", date
			if rules.days > 0 {
				v.why += fmt.Sprintf(", older than %d days", rules.days)
			}
			if rules.count > 0 {
				v.why += fmt.Sprintf(", not among the %d newest", rules.count)
			}
			candidates = append(candidates, tag)
		}
		p.verdicts[tag] = v
	}
	if len(candidates) == 0 {
		return p, nil
	}

	plan, err := registry.PlanDelete(ctx, api, repo, candidates)
	if err != nil {
		return repoPurge{}, err
	}
	for _, tag := range plan.Missing {
		p.verdicts[tag] = verdict{"skip", "its manifest is gone, so there's no image to delete"}
	}
	p.plan = registry.DeletePlan{Repo: repo}
	for _, img := range plan.Images {
		if !img.Whole() {
			kept := img.Tags[slices.IndexFunc(img.Tags, func(t string) bool { return !slices.Contains(img.Selected, t) })]
			for _, tag := range img.Selected {
				p.verdicts[tag] = verdict{"keep", "shares its image with kept tag " + kept}
			}
			continue
		}
		// A signature manifest that also carries an ordinary tag stays, or
		// deleting it would take that tag too.
		img.Signatures = slices.DeleteFunc(img.Signatures, func(s registry.PlannedSignature) bool {
			if i := slices.IndexFunc(s.Tags, func(t string) bool { return !registry.IsSignatureTag(t) }); i >= 0 {
				for _, tag := range s.Tags {
					if registry.IsSignatureTag(tag) {
						p.verdicts[tag] = verdict{"keep", "signature, shares its manifest with tag " + s.Tags[i]}
					}
				}
				return true
			}
			return false
		})
		for _, s := range img.Signatures {
			for _, tag := range s.Tags {
				p.verdicts[tag] = verdict{"delete", "signature of a deleted image"}
			}
		}
		p.plan.Images = append(p.plan.Images, img)
	}
	return p, nil
}

// tagCreated returns when tag's image was created, as the UI shows it, or
// zero if that's unknown: the tag has no platforms, or its manifest or
// config is gone.
func tagCreated(ctx context.Context, api *registry.API, repo, tag string) (time.Time, error) {
	m, err := api.Resolve(ctx, repo, tag)
	if errors.Is(err, registry.ErrNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	p, ok := registry.MainPlatform(m)
	if !ok {
		return time.Time{}, nil
	}
	c, err := registry.Created(ctx, api, repo, p)
	if errors.Is(err, registry.ErrNotFound) {
		return time.Time{}, nil
	}
	return c, err
}

// report prints a line per tag of the repository, after applying its plan
// when apply is set, and counts the tags and images deleted, or that would
// be, and the deletes that failed.
func (p repoPurge) report(stdout, stderr io.Writer, apply bool, results []registry.DeleteResult) (tags, images, failed int) {
	if apply {
		done := map[string]bool{}
		for _, r := range results {
			if r.Err != nil {
				failed++
				printf(stderr, "purge: deleting %s@%s (%s): %v\n", p.repo, r.Digest, strings.Join(r.Tags, ", "), r.Err)
				for _, tag := range r.Tags {
					p.verdicts[tag] = verdict{"failed", r.Err.Error()}
				}
				continue
			}
			for _, tag := range r.Tags {
				done[tag] = true
			}
			if !r.Signature {
				images++
			}
		}
		// A signature isn't tried when its image failed.
		for tag, v := range p.verdicts {
			if v.verb == "delete" && !done[tag] {
				p.verdicts[tag] = verdict{"keep", "signature of an image that failed to delete"}
			}
		}
	} else {
		images = len(p.plan.Images)
	}
	for _, tag := range p.tags {
		v := p.verdicts[tag]
		if v.verb == "delete" {
			tags++
		}
		printf(stdout, "%-6s %s:%s  (%s)\n", v.verb, p.repo, tag, v.why)
	}
	return tags, images, failed
}

// globs splits a comma-separated list of path.Match patterns, checking
// each. It returns nil for an empty list.
func globs(list string) ([]string, error) {
	var gs []string
	for g := range strings.SplitSeq(list, ",") {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		if _, err := path.Match(g, ""); err != nil {
			return nil, fmt.Errorf("%q: %w", g, err)
		}
		gs = append(gs, g)
	}
	return gs, nil
}

// matchAny reports whether repo matches one of globs.
func matchAny(globs []string, repo string) bool {
	return slices.ContainsFunc(globs, func(g string) bool {
		ok, _ := path.Match(g, repo)
		return ok
	})
}

// plural is "1 tag" or "3 tags".
func plural(n int, unit string) string {
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// printf writes purge's output. If the terminal or cron's mail is gone,
// there's no one left to tell.
func printf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
