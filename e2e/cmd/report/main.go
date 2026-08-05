// Command report renders, merges, cleans, or zips the vix e2e HTML report from
// the artifacts the harness appends. It is intentionally separate from the test
// binary so you can re-render or combine shard outputs without re-running tests.
//
// Usage:
//
//	report render --in <dir>
//	report merge  --in <dir1> --in <dir2> ... --out <dir>
//	report clean  --in <dir>
//	report zip    --in <dir> --out <file>
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/get-vix/vix/e2e/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)

	var ins multiFlag
	out := fs.String("out", "", "output path (zip file, or merged report dir)")
	fs.Var(&ins, "in", "report root directory (repeatable, for merge)")
	_ = fs.Parse(os.Args[2:])

	root := ""
	if len(ins) > 0 {
		root = ins[0]
	}

	var err error
	switch cmd {
	case "render":
		err = report.Render(root)
	case "clean":
		err = report.Clean(root)
	case "zip":
		if *out == "" {
			fail("zip requires --out")
		}
		err = report.Zip(root, *out)
	case "merge":
		if *out == "" {
			fail("merge requires --out")
		}
		err = report.Merge(*out, ins)
		if err == nil {
			err = report.Render(*out)
		}
	default:
		usage()
	}
	if err != nil {
		fail(err.Error())
	}
}

// multiFlag collects repeated --in values for merge.
type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func usage() {
	fmt.Fprint(os.Stderr, `report <command> [flags]

commands:
  render --in <dir>                 build <dir>/index.html from artifacts
  merge  --in <d1> --in <d2> --out <dir>   combine shard outputs then render
  clean  --in <dir>                 wipe results/ and images/ for a fresh run
  zip    --in <dir> --out <file>    package index.html + images/ into a zip
`)
	os.Exit(2)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "report:", msg)
	os.Exit(1)
}
