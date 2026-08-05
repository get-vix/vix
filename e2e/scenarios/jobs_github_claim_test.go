package scenarios

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This scenario pins the claim-before-act fix in the mission-control GitHub
// watch builder (internal/daemon/web/source/src/data/jobWorkflows.ts). It drives
// the REAL SQLite tracker.sh (begin/pick/claim) and the real select/claim step
// bash through the workflow engine, firing the job repeatedly via `vix job run`.
//
// The bug it guards (observed live: issue #59 triaged three runs in a row):
// dedup used to advance only in a trailing mark_done step AFTER the long agent
// step, so a run that died/raced after selecting an item left the cutoff
// unadvanced and the next run re-picked the same item. The fix CLAIMS the item
// (records it + advances the cutoff) right after selection, BEFORE `act`, and
// makes `pick` exclude any URL already recorded (so the scalar cutoff can no
// longer regress into a re-pick, and same-second items are neither lost nor
// duplicated).
//
// The graph mirrors the builder's shape:
//
//	begin -> fetch -> select -> claim -> act
//
// `fetch` is stubbed to pipe a test-controlled JSON feed through the real
// `tracker.sh pick` (no network/gh), and `act` stands in for the agent step: it
// only records which URL it processed and NEVER touches the tracker — so dedup
// is proven to come entirely from the pre-act `claim`. If the fix regressed to
// claim-after-act, an item would be re-picked every run and `processed.txt`
// would list it more than once.

const claimJobID = "e2e-claim"

// realTrackerSh is the emitted (unescaped) form of githubTrackerScript from the
// mission-control builder: a self-contained SQLite begin/pick/claim helper.
// `pick` excludes URLs already in the items table and walks with `>=`; `claim`
// records the item and advances the cutoff.
const realTrackerSh = `#!/usr/bin/env bash
set -euo pipefail

DB="${1:-}"
CMD="${2:-}"
[ -n "$DB" ] || { echo "usage: tracker.sh <db> <begin|pick|claim> [args]" >&2; exit 2; }
[ -n "$CMD" ] || { echo "tracker.sh: missing command" >&2; exit 2; }

now_ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
sqlq() { printf '%s' "${1:-}" | sed "s/'/''/g"; }

init() {
	sqlite3 "$DB" >/dev/null <<'SQL'
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=10000;
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS items(
  url TEXT PRIMARY KEY, number INTEGER, title TEXT,
  created_at TEXT, handled_at TEXT
);
SQL
}

case "$CMD" in
	begin)
		init
		now="$(now_ts)"
		cutoff="$(sqlite3 "$DB" "SELECT value FROM meta WHERE key='cutoff';")"
		if [ -z "$cutoff" ]; then
			sqlite3 "$DB" >/dev/null <<SQL
PRAGMA busy_timeout=10000;
BEGIN IMMEDIATE;
INSERT OR REPLACE INTO meta(key,value) VALUES('cutoff','$now');
INSERT OR REPLACE INTO meta(key,value) VALUES('last_run_at','$now');
COMMIT;
SQL
			echo FIRST_RUN
		else
			sqlite3 "$DB" >/dev/null <<SQL
PRAGMA busy_timeout=10000;
BEGIN IMMEDIATE;
INSERT OR REPLACE INTO meta(key,value) VALUES('last_run_at','$now');
COMMIT;
SQL
			echo "$cutoff"
		fi
		;;
	pick)
		cutoff="${3:-}"
		[ -n "$cutoff" ] || { echo "tracker.sh pick: missing cutoff" >&2; exit 2; }
		init
		handled="$(sqlite3 "$DB" "SELECT url FROM items;")"
		jq -c --arg c "$cutoff" --arg handled "$handled" '
			($handled | split("\n") | map(select(length > 0))) as $done
			| [ .[]
				| select(.createdAt != null and .createdAt >= $c)
				| select((.url // "") as $u | ($done | index($u)) == null) ]
			| sort_by(.createdAt, .number)
			| (.[0] // empty)
		'
		;;
	claim)
		url="${3:-}"
		created="${4:-}"
		number="${5:-0}"
		[ -n "$url" ] || { echo "tracker.sh claim: missing url" >&2; exit 2; }
		[ -n "$created" ] || { echo "tracker.sh claim: missing createdAt" >&2; exit 2; }
		case "$number" in '' | *[!0-9]*) number=0 ;; esac
		title="$(cat)"
		now="$(now_ts)"
		eurl="$(sqlq "$url")"
		ecreated="$(sqlq "$created")"
		etitle="$(sqlq "$title")"
		init
		sqlite3 "$DB" >/dev/null <<SQL
PRAGMA busy_timeout=10000;
BEGIN IMMEDIATE;
INSERT INTO items(url,number,title,created_at,handled_at)
  VALUES('$eurl',$number,'$etitle','$ecreated','$now')
  ON CONFLICT(url) DO UPDATE SET
    handled_at='$now', number=$number, title='$etitle', created_at='$ecreated';
INSERT OR REPLACE INTO meta(key,value) VALUES('cutoff','$ecreated');
COMMIT;
SQL
		;;
	*)
		echo "tracker.sh: unknown command '$CMD'" >&2
		exit 2
		;;
esac
`

// fetchStubClaimE2E stands in for the builder's fetch step: instead of calling
// gh/curl it pipes the test-controlled feed.json through the REAL `tracker.sh
// pick`, exactly as the builder pipes gh/curl output through pick.
const fetchStubClaimE2E = `cutoff=$(printf '%s' "$(step.begin)")
out="$(workflow.dir)/items.json"
: > "$out"
"$(workflow.dir)/tracker.sh" "$(workflow.dir)/tracker.db" pick "$cutoff" < feed.json > "$out" || true
cat "$out"`

// selectClaimE2E is the builder's select step verbatim (emitted form).
const selectClaimE2E = `out="$(workflow.dir)/items.json"
url=$(jq -r '.url // empty' "$out" 2>/dev/null)
if [ -z "$url" ]; then echo NO_TODO; exit 0; fi
echo "$url"`

// claimStepClaimE2E is the builder's claim step verbatim (emitted form): it
// records the selected item via the tracker BEFORE the agent step runs.
const claimStepClaimE2E = `out="$(workflow.dir)/items.json"
url=$(jq -r '.url // empty' "$out" 2>/dev/null)
created=$(jq -r '.createdAt // empty' "$out" 2>/dev/null)
number=$(jq -r '.number // 0' "$out" 2>/dev/null)
[ -n "$url" ] || exit 0
[ -n "$created" ] || exit 0
jq -r '.title // ""' "$out" 2>/dev/null | "$(workflow.dir)/tracker.sh" "$(workflow.dir)/tracker.db" claim "$url" "$created" "$number"`

// actStubClaimE2E stands in for the agent step: it records which URL it
// processed and NEVER touches the tracker, so any dedup must come from the
// pre-act claim. It runs in the job dir so the marker sits beside the tracker.
const actStubClaimE2E = `url="$(step.select)"
printf '%s\n' "${url%$'\n'}" >> "$(workflow.dir)/processed.txt"`

func claimJobSpec() string {
	spec := map[string]any{
		"id":          claimJobID,
		"name":        "Claim e2e",
		"enabled":     true,
		"trigger":     map[string]any{"type": "at", "time": "2099-01-01T00:00:00Z"},
		"prompt":      "claim-before-act e2e",
		"cwd":         "{{WORKDIR}}",
		"created_by":  "web",
		"permissions": map[string]any{"auto_write": true, "auto_dirs": true},
		"files": []any{
			map[string]any{"path": "tracker.sh", "content": realTrackerSh, "mode": "0755"},
		},
		"workflow": map[string]any{
			"name":        "e2e-claim-wf",
			"entry_point": map[string]any{"id": "begin"},
			"steps": map[string]any{
				"begin": map[string]any{
					"type":    "bash",
					"command": `"$(workflow.dir)/tracker.sh" "$(workflow.dir)/tracker.db" begin`,
					"next_steps": []any{map[string]any{
						"id":         "fetch",
						"execute_if": `[[ "$(step.begin)" != *FIRST_RUN* ]]`,
					}},
				},
				"fetch": map[string]any{
					"type":       "bash",
					"command":    fetchStubClaimE2E,
					"next_steps": []any{map[string]any{"id": "select"}},
				},
				"select": map[string]any{
					"type":    "bash",
					"command": selectClaimE2E,
					"next_steps": []any{map[string]any{
						"id":         "claim",
						"execute_if": `[[ "$(step.select)" != *NO_TODO* ]]`,
					}},
				},
				"claim": map[string]any{
					"type":       "bash",
					"command":    claimStepClaimE2E,
					"next_steps": []any{map[string]any{"id": "act"}},
				},
				"act": map[string]any{"type": "bash", "command": actStubClaimE2E},
			},
		},
	}
	b, _ := json.Marshal(spec)
	return string(b)
}

// TestJobGithubClaimBeforeAct fires the GitHub watch job repeatedly and asserts
// that each open item is processed exactly once, in oldest-first order, and
// never re-picked — even though the agent stand-in never marks the tracker.
// This is the direct guard for the claim-before-act + URL-exclusion fix.
func TestJobGithubClaimBeforeAct(t *testing.T) {
	// Two issues sharing the exact same createdAt second: the old strict-`>`
	// scalar-cutoff design would have lost the second one; URL exclusion keeps
	// both. Far-future createdAt keeps them eligible past the first-run baseline.
	const a = "https://github.com/o/r/issues/1"
	const b = "https://github.com/o/r/issues/2"
	feed := `[{"url":"` + a + `","number":1,"title":"first","createdAt":"2099-01-01T00:00:00Z"},` +
		`{"url":"` + b + `","number":2,"title":"second","createdAt":"2099-01-01T00:00:00Z"}]`

	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.github_claim",
		Description: "the GitHub watch job claims each item before acting, so an item is processed exactly once (oldest-first) and never re-picked even when the agent step never marks the tracker",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/"+claimJobID+"/job.json", claimJobSpec()),
		harness.WithWorkdirFile("feed.json", feed),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	processedPath := h.HomePath(".vix/jobs/" + claimJobID + "/processed.txt")
	readProcessed := func() string {
		bs, err := os.ReadFile(processedPath)
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(bs), "\n")
	}

	fire := func(runNo int) {
		out, err := h.RunCLI("job", "run", claimJobID)
		if err != nil {
			t.Fatalf("run %d: vix job run failed: %v\n%s", runNo, err, out)
		}
		if !pollUntil(30*time.Second, func() bool { return countStateRuns(h, claimJobID) >= runNo }) {
			t.Fatalf("run %d did not complete; processed=%q\n%s", runNo, readProcessed(), h.Daemon.LogTail(80))
		}
	}

	// Run 1: begin prints FIRST_RUN and stops — the baseline is seeded, nothing
	// is handled, so the agent stand-in never runs.
	fire(1)
	if got := readProcessed(); got != "" {
		t.Fatalf("after the baseline run processed.txt should be empty, got %q", got)
	}

	// Run 2: A is the oldest eligible item; it is claimed then processed.
	fire(2)
	if got := readProcessed(); got != a {
		t.Fatalf("after run 2 processed =\n%q\nwant\n%q", got, a)
	}

	// Run 3: A is already recorded, so pick excludes it (despite B sharing A's
	// createdAt second) and returns B. Same-second item is NOT lost.
	fire(3)
	if got, want := readProcessed(), a+"\n"+b; got != want {
		t.Fatalf("after run 3 processed =\n%q\nwant\n%q", got, want)
	}

	// Run 4: both items are recorded, so the run selects NO_TODO and the agent
	// stand-in does not run. Nothing is ever re-picked.
	fire(4)
	if got, want := readProcessed(), a+"\n"+b; got != want {
		t.Fatalf("after run 4 an item was re-picked:\ngot  %q\nwant %q (unchanged)", got, want)
	}
}
