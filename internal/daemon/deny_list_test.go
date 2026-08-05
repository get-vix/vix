package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testRoot returns a fully-symlink-resolved temp dir so comparisons in
// isPathDenied (which also resolves symlinks) don't silently differ on
// macOS where /tmp and /var/folders are themselves symlinked.
//
// On some macOS configurations (e.g. when /private is not traversable),
// EvalSymlinks fails. In that case the unresolved path is returned, which
// is consistent with the fallback behaviour in isPathDenied itself.
func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return root
	}
	return real
}

// --- A. isPathDenied ----------------------------------------------------

func TestIsPathDenied_ExactAndDescendant(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secrets, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	deny := []string{secrets}

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact", secrets, true},
		{"direct child", filepath.Join(secrets, "api.txt"), true},
		{"nested child", filepath.Join(secrets, "sub", "a.txt"), true},
		{"sibling prefix", filepath.Join(root, "secretsX"), false},
		{"parent of entry", root, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := isPathDenied(c.in, root, deny)
			if got != c.want {
				t.Errorf("isPathDenied(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsPathDenied_EmptyList(t *testing.T) {
	if denied, _ := isPathDenied("/any/path", "/cwd", nil); denied {
		t.Error("empty deny list should never deny")
	}
	if denied, _ := isPathDenied("", "/cwd", []string{"/x"}); denied {
		t.Error("empty input should never deny")
	}
}

func TestIsPathDenied_SymlinkIntoDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks not supported on windows in CI")
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	apiFile := filepath.Join(secrets, "api.txt")
	if err := os.WriteFile(apiFile, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link_to_api")
	if err := os.Symlink(apiFile, link); err != nil {
		t.Fatal(err)
	}
	deny := []string{secrets}
	if denied, _ := isPathDenied(link, root, deny); !denied {
		t.Errorf("symlink pointing into denied tree should be blocked")
	}
}

func TestIsPathDenied_SymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	os.WriteFile(filepath.Join(secrets, "x"), []byte("y"), 0o600)
	dlink := filepath.Join(root, "dlink")
	if err := os.Symlink(secrets, dlink); err != nil {
		t.Fatal(err)
	}
	deny := []string{secrets}
	if denied, _ := isPathDenied(filepath.Join(dlink, "x"), root, deny); !denied {
		t.Errorf("file under symlinked denied dir should be blocked")
	}
}

func TestIsPathDenied_DenyEntryIsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(secrets, alias); err != nil {
		t.Fatal(err)
	}
	deny := []string{alias}
	if denied, _ := isPathDenied(filepath.Join(secrets, "api.txt"), root, deny); !denied {
		t.Errorf("deny entry declared via symlink should still match real path")
	}
}

func TestIsPathDenied_TrailingSlashAndDoubleDot(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(filepath.Join(root, "foo"), 0o755)

	// Trailing slash on entry is normalized by filepath.Clean.
	denyTrail := []string{secrets + string(filepath.Separator)}
	if denied, _ := isPathDenied(filepath.Join(secrets, "api.txt"), root, denyTrail); !denied {
		t.Error("trailing-slash entry should still match descendant")
	}

	// `..` traversal in input.
	deny := []string{secrets}
	traversed := filepath.Join(root, "foo", "..", "secrets", "api.txt")
	if denied, _ := isPathDenied(traversed, root, deny); !denied {
		t.Error("'..'-traversed path should be denied after Clean")
	}
}

// --- B. checkDenyList ---------------------------------------------------

func TestCheckDenyList_FileTools_PathVariants(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	os.WriteFile(filepath.Join(secrets, "api.txt"), []byte("k"), 0o600)
	os.WriteFile(filepath.Join(safe, "ok.txt"), []byte("ok"), 0o600)
	deny := []string{secrets}

	tools := []string{"read_file", "write_file", "edit_file", "delete_file", "write_minified_file", "edit_minified_file"}

	type variant struct {
		name     string
		path     string
		wantDeny bool
	}
	variants := []variant{
		{"absolute denied", filepath.Join(secrets, "api.txt"), true},
		{"dot-relative denied", "./secrets/api.txt", true},
		{"bare-name denied", "secrets/api.txt", true},
		{"dotdot-traversal denied", "./safe/../secrets/api.txt", true},
		{"absolute safe", filepath.Join(safe, "ok.txt"), false},
		{"relative safe", "./safe/ok.txt", false},
	}

	for _, tool := range tools {
		for _, v := range variants {
			t.Run(tool+"/"+v.name, func(t *testing.T) {
				params := map[string]any{"path": v.path}
				res := checkDenyList(tool, params, root, deny, nil)
				if v.wantDeny {
					if res == nil {
						t.Fatalf("%s with path %q: expected deny, got nil", tool, v.path)
					}
					if !res.IsError {
						t.Error("expected IsError=true")
					}
				} else {
					if res != nil {
						t.Fatalf("%s with path %q: expected allow, got %+v", tool, v.path, res)
					}
				}
			})
		}
	}
}

func TestCheckDenyList_FileTools_SymlinkIntoDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	os.WriteFile(filepath.Join(secrets, "api.txt"), []byte("k"), 0o600)
	link := filepath.Join(root, "link_to_api")
	if err := os.Symlink(filepath.Join(secrets, "api.txt"), link); err != nil {
		t.Fatal(err)
	}

	res := checkDenyList("read_file", map[string]any{"path": link}, root, []string{secrets}, nil)
	if res == nil {
		t.Fatal("expected deny via symlink")
	}
}

func TestCheckDenyList_EmptyDenyList(t *testing.T) {
	res := checkDenyList("read_file", map[string]any{"path": "/anything"}, "/cwd", nil, nil)
	if res != nil {
		t.Errorf("no deny list should allow anything, got %+v", res)
	}
}

func TestCheckDenyList_UnknownTool(t *testing.T) {
	// Tools that aren't file-path or bash are passed through (no schema for
	// a path param). `grep`/`glob_files` filter at output time, not here.
	res := checkDenyList("grep", map[string]any{"path": "/tmp/x"}, "/cwd", []string{"/tmp/x"}, nil)
	if res != nil {
		t.Errorf("grep should not be gated by checkDenyList, got %+v", res)
	}
}

// Bash table: best-effort path-like token scan. Bare tokens match only an
// exact deny entry that is a regular file; denied directory names in prose do
// not count as paths.
func TestCheckDenyList_Bash_Table(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	secretFile := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	deny := []string{secrets, secretFile}

	cases := []struct {
		name    string
		command string
		deny    bool
	}{
		// --- obvious path references: must deny ---
		{"absolute path", "cat " + filepath.Join(secrets, "api.txt"), true},
		{"dot-relative", "cat ./secrets/api.txt", true},
		{"bare relative", "cat secrets/api.txt", true},
		{"bare denied file", "cat secret.txt", true},
		{"dotdot-relative", "cat ../" + filepath.Base(root) + "/secrets/api.txt", true},
		{"ls dir", "ls -la " + secrets, true},
		{"grep into dir", "grep foo " + secrets + "/", true},
		{"multi statement", "cat safe/ok.txt && cat secrets/x", true},
		{"piped redirect", "echo hi | tee secrets/out", true},
		{"stdout redirect", "echo hi > secrets/out", true},
		{"double-quoted path", `cat "secrets/api.txt"`, true},
		{"single-quoted path", `cat 'secrets/api.txt'`, true},
		{"backtick subshell", "echo `cat secrets/x`", true},
		{"dollar subshell", "echo $(cat secrets/x)", true},

		// --- allowed ---
		{"sibling prefix", "cat secretsX/api.txt", false},
		{"prose with secrets word", `echo 'I have no secrets'`, false},
		{"bare word secrets", "echo secrets is a word", false},
		{"bare word cat", "cat secrets", false}, // documented v1 limitation
		{"absolute safe", "cat " + filepath.Join(safe, "ok.txt"), false},
		{"cd then relative", "cd " + safe + " && cat ok.txt", false},
		{"var expansion", "VAR=secrets/x; cat $VAR", false}, // documented v1 limitation
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := map[string]any{"command": c.command}
			res := checkDenyList("bash", params, root, deny, nil)
			if c.deny && res == nil {
				t.Fatalf("command %q: expected deny, got allow", c.command)
			}
			if !c.deny && res != nil {
				t.Fatalf("command %q: expected allow, got deny (%s)", c.command, res.Output)
			}
		})
	}
}

// --- B'. URL deny check -------------------------------------------------
//
// The URL matcher has two modes (host-suffix when the entry is bare, prefix
// when the entry has a scheme) and a few normalization rules (case, ports,
// userinfo, query/fragment). The tests below pin each of those down.

func TestIsURLDenied_HostnameMatch(t *testing.T) {
	deny := []string{"example.com"}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/", true},
		{"https://example.com", true},
		{"http://example.com/path?q=1", true},
		{"https://api.example.com/x", true}, // dot-aligned suffix
		{"https://EXAMPLE.com/", true},      // case-insensitive host
		{"https://notexample.com/", false},  // not dot-aligned
		{"https://example.com.evil.org/", false},
		{"https://other.com/", false},
	}
	for _, c := range cases {
		got, _ := isURLDenied(c.url, deny)
		if got != c.want {
			t.Errorf("isURLDenied(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestIsURLDenied_SchemePrefixMatch(t *testing.T) {
	deny := []string{"https://example.com/admin"}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/admin", true},
		{"https://example.com/admin/", true},
		{"https://example.com/admin/users", true},
		{"https://example.com/", false},          // host matches but path doesn't
		{"https://example.com/public", false},    // outside admin path
		{"http://example.com/admin", false},      // scheme mismatch (entry is https)
		{"https://api.example.com/admin", false}, // host mismatch (prefix is exact)
	}
	for _, c := range cases {
		got, _ := isURLDenied(c.url, deny)
		if got != c.want {
			t.Errorf("isURLDenied(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestIsURLDenied_EmptyAndMalformed(t *testing.T) {
	if denied, _ := isURLDenied("", []string{"example.com"}); denied {
		t.Error("empty url should never deny")
	}
	if denied, _ := isURLDenied("https://example.com/", nil); denied {
		t.Error("empty deny list should never deny")
	}
	// Malformed URL: parser may fail or yield empty host. Substring fallback
	// must still catch obvious matches.
	if denied, _ := isURLDenied("not a url example.com whatever", []string{"example.com"}); !denied {
		t.Error("substring fallback should catch entry within malformed input")
	}
}

func TestCheckDenyList_WebFetch_BlocksDeniedURL(t *testing.T) {
	deny := []string{"bad.example.com"}
	res := checkDenyList("web_fetch", map[string]any{"url": "https://api.bad.example.com/leak"}, "/cwd", nil, deny)
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
}

func TestCheckDenyList_WebFetch_AllowsOtherURL(t *testing.T) {
	deny := []string{"bad.example.com"}
	res := checkDenyList("web_fetch", map[string]any{"url": "https://safe.example.org/x"}, "/cwd", nil, deny)
	if res != nil {
		t.Fatalf("expected allow, got %+v", res)
	}
}

func TestCheckDenyList_Bash_URL_Table(t *testing.T) {
	deny := []string{"bad.example.com", "https://example.org/admin"}

	cases := []struct {
		name    string
		command string
		deny    bool
	}{
		{"curl denied host", "curl https://bad.example.com/x", true},
		{"curl denied host quoted", `curl "https://bad.example.com/x"`, true},
		{"curl single-quoted", `curl 'https://api.bad.example.com/x'`, true},
		{"wget denied", "wget -O - https://api.bad.example.com/leak", true},
		{"piped curl", "curl https://bad.example.com/x | tee out", true},
		{"path-prefix denied", "curl https://example.org/admin/users", true},
		{"path-prefix not under admin", "curl https://example.org/public", false},
		{"safe host", "curl https://safe.example.org/x", false},
		{"prose with bad in string", `echo "this is not bad.example.com just text"`, false},
		{"no url at all", "echo hello", false},
		{"backtick subshell url", "echo `curl https://bad.example.com/x`", true},
		{"dollar subshell url", "echo $(curl https://bad.example.com/x)", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := map[string]any{"command": c.command}
			res := checkDenyList("bash", params, "/cwd", nil, deny)
			if c.deny && res == nil {
				t.Fatalf("command %q: expected deny, got allow", c.command)
			}
			if !c.deny && res != nil {
				t.Fatalf("command %q: expected allow, got deny: %s", c.command, res.Output)
			}
		})
	}
}

// Hostname matching ignores port, userinfo, query, and fragment — the
// match is on the registered domain only. Important so that an attacker
// can't trivially bypass a `bad.example.com` entry by appending a port
// or smuggling credentials.
func TestIsURLDenied_HostnameIgnoresPortUserinfoQueryFragment(t *testing.T) {
	deny := []string{"bad.example.com"}
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"port", "https://bad.example.com:8443/x", true},
		{"port on subdomain", "https://api.bad.example.com:8080/x", true},
		{"userinfo", "https://user:pass@bad.example.com/x", true},
		{"userinfo plus port", "https://user@bad.example.com:9000/x", true},
		{"query string", "https://bad.example.com/?token=secret", true},
		{"fragment", "https://bad.example.com/path#section", true},
		{"query + fragment + port", "https://bad.example.com:8080/p?a=1#frag", true},
		{"trailing dot in host", "https://bad.example.com./x", true},
		{"unrelated host with port", "https://other.com:8443/x", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := isURLDenied(c.url, deny)
			if got != c.want {
				t.Errorf("isURLDenied(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

// Scheme-prefix entries with a port match only when the input URL carries
// the same port. A bare-host entry, in contrast, would match either form
// (covered in the Hostname test above).
func TestIsURLDenied_SchemePrefixWithPort(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		url   string
		want  bool
	}{
		{"port match", "https://example.com:8080/admin", "https://example.com:8080/admin/users", true},
		{"port mismatch", "https://example.com:8080/admin", "https://example.com:9000/admin", false},
		{"explicit default port matches default", "https://example.com:443/admin", "https://example.com/admin", false},
		{"prefix path with trailing slash", "https://example.com/admin/", "https://example.com/admin/users", true},
		{"prefix path partial path segment", "https://example.com/admin", "https://example.com/administrator", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := isURLDenied(c.url, []string{c.entry})
			if got != c.want {
				t.Errorf("entry=%q url=%q got %v want %v", c.entry, c.url, got, c.want)
			}
		})
	}
}

// Multiple entries: any matching entry causes a deny, and the matching
// entry is reported back so error messages stay actionable.
func TestIsURLDenied_MultipleEntriesAndReturnedEntry(t *testing.T) {
	deny := []string{"safe.example.com", "bad.example.com", "https://other.com/admin"}

	denied, entry := isURLDenied("https://api.bad.example.com/x", deny)
	if !denied {
		t.Fatal("expected deny via bad.example.com")
	}
	if entry != "bad.example.com" {
		t.Errorf("expected returned entry %q, got %q", "bad.example.com", entry)
	}

	denied, entry = isURLDenied("https://other.com/admin/users", deny)
	if !denied {
		t.Fatal("expected deny via prefix entry")
	}
	if entry != "https://other.com/admin" {
		t.Errorf("expected returned entry %q, got %q", "https://other.com/admin", entry)
	}

	if denied, _ := isURLDenied("https://harmless.example.org/", deny); denied {
		t.Error("none of the entries should match")
	}
}

// Entries with surrounding whitespace and mixed casing must still match.
// Scheme/host are matched case-insensitively; path is case-sensitive — a
// deliberate choice since URL paths are spec-wise case-sensitive and a
// case-insensitive path match would silently broaden deny rules in ways
// the operator didn't write down.
func TestIsURLDenied_EntryWhitespaceAndCase(t *testing.T) {
	// Bare-host entry: whitespace + case must not break the match.
	if denied, _ := isURLDenied("https://example.com/", []string{"  EXAMPLE.com  "}); !denied {
		t.Error("entry whitespace + case should still match host")
	}

	// Scheme-prefix entry: scheme/host case-insensitive, path case-sensitive.
	prefix := []string{"HTTPS://API.example.com/Admin"}
	if denied, _ := isURLDenied("https://api.example.com/Admin/users", prefix); !denied {
		t.Error("scheme-prefix case-insensitive scheme/host match failed")
	}
	// Differs only in path casing — must NOT match (case-sensitive path).
	if denied, entry := isURLDenied("https://api.example.com/admin/users", prefix); denied {
		t.Errorf("path is intentionally case-sensitive — should not match (got entry %q)", entry)
	}
}

// Empty / blank entries must be ignored rather than acting as a wildcard.
// Without this the loader's TrimSpace would leak through to the matcher
// and a stray "" entry could accidentally deny everything.
func TestIsURLDenied_BlankEntryIgnored(t *testing.T) {
	deny := []string{"", "   ", "bad.example.com"}
	if denied, _ := isURLDenied("https://safe.example.org/", deny); denied {
		t.Error("blank entries must not match anything")
	}
	if denied, _ := isURLDenied("https://bad.example.com/", deny); !denied {
		t.Error("real entry should still match alongside blank entries")
	}
}

// IP-literal hostnames need to behave like normal hostnames: an entry of
// "127.0.0.1" matches its URL forms, but does NOT match a different IP.
func TestIsURLDenied_IPLiteralHost(t *testing.T) {
	deny := []string{"127.0.0.1"}
	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1/", true},
		{"http://127.0.0.1:8080/x", true},
		{"http://127.0.0.2/", false},
		// IPv6 isn't a regression target for v1; document the current
		// behaviour with one no-match case so a future change tightening
		// this is forced to reckon with the test.
		{"http://[::1]/", false},
	}
	for _, c := range cases {
		got, _ := isURLDenied(c.url, deny)
		if got != c.want {
			t.Errorf("isURLDenied(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// Bash URL scan: tokens may carry surrounding shell punctuation that the
// splitter is responsible for stripping (`;`, `&&`, redirects). Verify each
// of those still produces a denial.
func TestCheckDenyList_Bash_URL_AdjacentPunctuation(t *testing.T) {
	deny := []string{"bad.example.com"}
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"trailing semicolon", "curl https://bad.example.com/x; echo done", true},
		{"trailing logical-and", "curl https://bad.example.com/x && echo ok", true},
		{"leading parenthesis", "(curl https://bad.example.com/x)", true},
		{"redirect to file", "curl https://bad.example.com/x > out.txt", true},
		{"chained subshell", "echo $(curl https://bad.example.com/x | head)", true},
		{"safe url after denied url", "curl https://bad.example.com/a; curl https://safe.org/b", true},
		{"safe url before denied url", "curl https://safe.org/a; curl https://bad.example.com/b", true},
		{"all safe urls", "curl https://safe.org/a; curl https://safer.org/b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := checkDenyList("bash", map[string]any{"command": c.command}, "/cwd", nil, deny)
			if c.want && res == nil {
				t.Fatalf("command %q: expected deny, got allow", c.command)
			}
			if !c.want && res != nil {
				t.Fatalf("command %q: expected allow, got deny: %s", c.command, res.Output)
			}
		})
	}
}

// Path and URL deny lists are checked in tandem; verify a single bash call
// can be denied by either dimension and that path-deny short-circuits
// before url-deny (path errors are more actionable).
func TestCheckDenyList_Bash_PathAndURLCombined(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)

	denyPaths := []string{secrets}
	denyURLs := []string{"bad.example.com"}

	// Path-only command denied.
	if res := checkDenyList("bash", map[string]any{"command": "cat secrets/x"}, root, denyPaths, denyURLs); res == nil {
		t.Error("path-only command should be denied")
	}
	// URL-only command denied.
	if res := checkDenyList("bash", map[string]any{"command": "curl https://bad.example.com/x"}, root, denyPaths, denyURLs); res == nil {
		t.Error("url-only command should be denied")
	}
	// Mixed: path takes priority in error message but either deny short-circuits.
	res := checkDenyList("bash", map[string]any{"command": "curl https://bad.example.com/x > secrets/out"}, root, denyPaths, denyURLs)
	if res == nil {
		t.Fatal("mixed command should be denied")
	}
	if !strings.Contains(res.Output, "secrets") {
		t.Errorf("expected path entry to surface first, got %q", res.Output)
	}
}

// --- C. filterOutputAgainstDeny -----------------------------------------

func TestFilterOutput_GrepPathLinenoContent(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	deny := []string{secrets}

	lines := []string{
		filepath.Join(safe, "a.txt") + ":1:hello",
		filepath.Join(secrets, "b.txt") + ":2:nope",
		filepath.Join(safe, "c.txt") + ":3:world",
	}
	out := strings.Join(lines, "\n")
	got := filterOutputAgainstDeny(out, root, deny)

	if strings.Contains(got, "secrets/b.txt") {
		t.Errorf("denied line leaked: %q", got)
	}
	if !strings.Contains(got, "a.txt:1:hello") || !strings.Contains(got, "c.txt:3:world") {
		t.Errorf("safe lines missing: %q", got)
	}
}

func TestFilterOutput_RelativePathsInOutput(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	deny := []string{secrets}

	// When grep is invoked with cwd=root and a relative path, its output is
	// also relative. filterOutputAgainstDeny must resolve against cwd.
	lines := []string{
		"safe/a.txt:1:hello",
		"secrets/b.txt:2:nope",
		"safe/c.txt:3:world",
	}
	got := filterOutputAgainstDeny(strings.Join(lines, "\n"), root, deny)
	if strings.Contains(got, "secrets/b.txt") {
		t.Errorf("denied relative line leaked: %q", got)
	}
	if !strings.Contains(got, "safe/a.txt") || !strings.Contains(got, "safe/c.txt") {
		t.Errorf("safe lines missing: %q", got)
	}
}

func TestFilterOutput_GlobOnePathPerLine(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	deny := []string{secrets}

	lines := []string{
		filepath.Join(safe, "a.txt"),
		filepath.Join(secrets, "b.txt"),
		filepath.Join(safe, "c.txt"),
	}
	got := filterOutputAgainstDeny(strings.Join(lines, "\n"), root, deny)
	if strings.Contains(got, "secrets/b.txt") {
		t.Errorf("denied path leaked in glob filter: %q", got)
	}
	if !strings.Contains(got, "a.txt") || !strings.Contains(got, "c.txt") {
		t.Errorf("safe paths missing: %q", got)
	}
}

func TestFilterOutput_GroupSeparatorCollapse(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	deny := []string{secrets}

	// Two grep match groups separated by "--"; denied group in the middle
	// must leave no orphan separator.
	input := strings.Join([]string{
		filepath.Join(safe, "a.txt") + ":1:hello",
		"--",
		filepath.Join(secrets, "b.txt") + ":2:nope",
		"--",
		filepath.Join(safe, "c.txt") + ":3:world",
	}, "\n")
	got := filterOutputAgainstDeny(input, root, deny)
	// No consecutive "--" lines should remain.
	if strings.Contains(got, "--\n--") {
		t.Errorf("consecutive separators not collapsed: %q", got)
	}
	// No trailing or leading "--".
	if strings.HasPrefix(got, "--") {
		t.Errorf("leading separator not removed: %q", got)
	}
	if strings.HasSuffix(got, "--") {
		t.Errorf("trailing separator not removed: %q", got)
	}
}

func TestFilterOutput_EmptyAndAllDenied(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	deny := []string{secrets}

	if got := filterOutputAgainstDeny("", root, deny); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
	input := filepath.Join(secrets, "a.txt") + ":1:x\n" + filepath.Join(secrets, "b.txt") + ":2:y"
	got := filterOutputAgainstDeny(input, root, deny)
	if strings.Contains(got, "secrets/") {
		t.Errorf("all-denied output should be empty, got %q", got)
	}
}

func TestFilterOutput_NoDenyList(t *testing.T) {
	input := "foo.txt:1:hello"
	if got := filterOutputAgainstDeny(input, "/cwd", nil); got != input {
		t.Errorf("no deny list should pass through unchanged, got %q", got)
	}
}

// --- D. Config loader merge ---------------------------------------------

// New schema: deny_list as an object with paths and urls.
func TestLoadProjectConfig_DenyList_NewSchema(t *testing.T) {
	root := testRoot(t)
	abs := filepath.Join(root, "abs_denied")
	os.MkdirAll(abs, 0o755)
	projectDir := filepath.Join(root, "project")
	os.MkdirAll(projectDir, 0o755)
	relTarget := filepath.Join(projectDir, "rel_denied")
	os.MkdirAll(relTarget, 0o755)

	// Project-level config: one absolute path, one relative path, one URL.
	projectCfg := `{
		"version": ` + currentVersionStr() + `,
		"deny_list": {
			"paths": ["` + abs + `", "./rel_denied"],
			"urls": ["bad.example.com", "https://api.example.com/admin"]
		}
	}`
	projectPath := filepath.Join(projectDir, "settings.json")
	if err := os.WriteFile(projectPath, []byte(projectCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Home config: extra path + duplicate path + extra URL + duplicate URL.
	homeDir := filepath.Join(root, "home")
	os.MkdirAll(homeDir, 0o755)
	homeTarget := filepath.Join(root, "home_denied")
	os.MkdirAll(homeTarget, 0o755)
	homeCfg := `{
		"version": ` + currentVersionStr() + `,
		"deny_list": {
			"paths": ["` + homeTarget + `", "` + abs + `"],
			"urls": ["another-bad.example.org", "bad.example.com"]
		}
	}`
	homePath := filepath.Join(homeDir, "settings.json")
	if err := os.WriteFile(homePath, []byte(homeCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadProjectConfig(homePath, projectPath)
	if len(cfg.DenyPaths) != 3 {
		t.Fatalf("expected 3 unique path entries, got %d: %v", len(cfg.DenyPaths), cfg.DenyPaths)
	}
	wantPaths := map[string]bool{abs: true, relTarget: true, homeTarget: true}
	for _, d := range cfg.DenyPaths {
		if !wantPaths[d] {
			t.Errorf("unexpected path entry %q", d)
		}
		delete(wantPaths, d)
	}
	for missing := range wantPaths {
		t.Errorf("missing path entry %q", missing)
	}

	if len(cfg.DenyURLs) != 3 {
		t.Fatalf("expected 3 unique url entries, got %d: %v", len(cfg.DenyURLs), cfg.DenyURLs)
	}
	wantURLs := map[string]bool{
		"bad.example.com":               true,
		"https://api.example.com/admin": true,
		"another-bad.example.org":       true,
	}
	for _, u := range cfg.DenyURLs {
		if !wantURLs[u] {
			t.Errorf("unexpected url entry %q", u)
		}
		delete(wantURLs, u)
	}
	for missing := range wantURLs {
		t.Errorf("missing url entry %q", missing)
	}
}

// Legacy schema: a flat string array under deny_list still parses as paths.
// Lets existing user configs keep working after the schema upgrade.
func TestLoadProjectConfig_DenyList_LegacyArray(t *testing.T) {
	root := testRoot(t)
	abs := filepath.Join(root, "abs_denied")
	os.MkdirAll(abs, 0o755)
	cfgPath := filepath.Join(root, "settings.json")
	content := `{"version": ` + currentVersionStr() + `, "deny_list": ["` + abs + `"]}`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	cfg := LoadProjectConfig(cfgPath)
	if len(cfg.DenyPaths) != 1 || cfg.DenyPaths[0] != abs {
		t.Errorf("legacy array should populate DenyPaths, got %+v", cfg.DenyPaths)
	}
	if len(cfg.DenyURLs) != 0 {
		t.Errorf("legacy array should leave DenyURLs empty, got %+v", cfg.DenyURLs)
	}
}

func TestLoadProjectConfig_DenyListAbsent(t *testing.T) {
	root := testRoot(t)
	cfgPath := filepath.Join(root, "settings.json")
	content := `{"version": ` + currentVersionStr() + `}`
	os.WriteFile(cfgPath, []byte(content), 0o644)
	cfg := LoadProjectConfig(cfgPath)
	if len(cfg.DenyPaths) != 0 {
		t.Errorf("expected empty path deny list, got %v", cfg.DenyPaths)
	}
	if len(cfg.DenyURLs) != 0 {
		t.Errorf("expected empty url deny list, got %v", cfg.DenyURLs)
	}
}

func currentVersionStr() string {
	// Rendered inline so the test tracks CurrentConfigVersion automatically.
	return itoa(CurrentConfigVersion)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// --- G. relative deny paths + tilde expansion (issue #52) ---------------

func TestExpandTildePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	cases := []struct {
		in, want string
	}{
		{"~", home},
		{"~/.ssh", filepath.Join(home, ".ssh")},
		{"~/a/b", filepath.Join(home, "a", "b")},
		{"~user/x", "~user/x"}, // only bare ~ / ~/ are expanded
		{"/abs/path", "/abs/path"},
		{".envrc.private", ".envrc.private"},
		{"", ""},
	}
	for _, c := range cases {
		if got := expandTildePath(c.in); got != c.want {
			t.Errorf("expandTildePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A relative entry declared in a ./.vix/settings.json is resolved BOTH against
// the config dir (existing behaviour) and recorded raw so the thread can also
// resolve it against cwd. This is the crux of issue #52.
func TestLoadProjectConfig_DenyList_RelativeDualResolution(t *testing.T) {
	root := testRoot(t)
	vixDir := filepath.Join(root, ".vix")
	os.MkdirAll(vixDir, 0o755)
	cfgPath := filepath.Join(vixDir, "settings.json")
	content := `{"version": ` + currentVersionStr() + `, "deny_list": {"paths": [".envrc.private"]}}`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	cfg := LoadProjectConfig(cfgPath)

	// Config-dir-relative form is still present (backward compatible).
	wantConfigDir := filepath.Join(vixDir, ".envrc.private")
	if len(cfg.DenyPaths) != 1 || cfg.DenyPaths[0] != wantConfigDir {
		t.Fatalf("DenyPaths = %v, want [%q]", cfg.DenyPaths, wantConfigDir)
	}
	// Raw relative form is preserved for cwd resolution.
	if len(cfg.DenyPathsRel) != 1 || cfg.DenyPathsRel[0] != ".envrc.private" {
		t.Fatalf("DenyPathsRel = %v, want [\".envrc.private\"]", cfg.DenyPathsRel)
	}
}

// A ~ entry is expanded to an absolute path under $HOME — not the phantom
// <configdir>/~/.ssh that plain filepath.Join produced before, which never
// matched the real ~/.ssh (the deny list silently protected nothing).
func TestLoadProjectConfig_DenyList_TildeExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	root := testRoot(t)
	vixDir := filepath.Join(root, ".vix")
	os.MkdirAll(vixDir, 0o755)
	cfgPath := filepath.Join(vixDir, "settings.json")
	content := `{"version": ` + currentVersionStr() + `, "deny_list": {"paths": ["~/.ssh"]}}`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	cfg := LoadProjectConfig(cfgPath)

	want := filepath.Join(home, ".ssh")
	if len(cfg.DenyPaths) != 1 || cfg.DenyPaths[0] != want {
		t.Fatalf("DenyPaths = %v, want [%q]", cfg.DenyPaths, want)
	}
	for _, p := range cfg.DenyPaths {
		if strings.Contains(p, "~") {
			t.Errorf("tilde not expanded: %q", p)
		}
	}
	// Absolute after expansion — not recorded as a raw relative entry.
	if len(cfg.DenyPathsRel) != 0 {
		t.Errorf("DenyPathsRel should be empty for ~ entry, got %v", cfg.DenyPathsRel)
	}
}

func TestCombineDenyPaths(t *testing.T) {
	cwd := "/proj"
	got := combineDenyPaths(cwd,
		[]string{"/proj/.vix/.envrc.private"}, // config-dir form
		[]string{".envrc.private", "sub/x"},   // raw relative
	)
	want := map[string]bool{
		"/proj/.vix/.envrc.private": true,
		"/proj/.envrc.private":      true,
		"/proj/sub/x":               true,
	}
	if len(got) != len(want) {
		t.Fatalf("combineDenyPaths = %v, want keys %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected entry %q", p)
		}
	}
}

// End-to-end at the thread layer: the exact scenario from issue #52. A
// relative deny entry in ./.vix/settings.json must block the secret file at
// the project root, and its contents must never leak.
func TestIntegration_DenyList_RelativeEntryBlocksProjectRoot(t *testing.T) {
	root := testRoot(t)
	vixDir := filepath.Join(root, ".vix")
	os.MkdirAll(vixDir, 0o755)
	cfgPath := filepath.Join(vixDir, "settings.json")
	os.WriteFile(cfgPath, []byte(`{"version": `+currentVersionStr()+`, "deny_list": {"paths": [".envrc.private"]}}`), 0o644)

	secret := filepath.Join(root, ".envrc.private")
	os.WriteFile(secret, []byte("API_KEY=SUPER_SECRET"), 0o600)

	cfg := LoadProjectConfig(cfgPath)
	deny := combineDenyPaths(root, cfg.DenyPaths, cfg.DenyPathsRel)
	s := newIntegrationThread(t, root, deny)

	for _, p := range []string{secret, ".envrc.private", "./.envrc.private"} {
		res := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": p})
		if res == nil || !res.IsError {
			t.Fatalf("path %q: expected deny error, got %+v", p, res)
		}
		if strings.Contains(res.Output, "SUPER_SECRET") {
			t.Errorf("path %q: secret leaked in output", p)
		}
	}
}
