// Package report renders the e2e HTML report from the per-test artifacts the
// harness appends. It is deliberately decoupled from the test binary: tests
// only write immutable, slug-keyed artifacts; this package globs and renders
// them idempotently, so it can run after a single container or after merging
// several sharded outputs.
package report

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/get-vix/vix/e2e/artifact"
)

// Render builds <root>/index.html from the artifacts under <root>/results.
func Render(root string) error {
	results, err := artifact.Load(root)
	if err != nil {
		return err
	}
	page := buildPage(results)
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, page); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "index.html"), buf.Bytes(), 0o644)
}

// Clean removes the results/ and images/ trees so the next run starts fresh.
func Clean(root string) error {
	for _, d := range []string{artifact.ResultsDir(root), artifact.ImagesDir(root)} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

// Merge copies results/ and images/ from each input report root into out, so a
// subsequent Render produces one combined report from sharded runs.
func Merge(out string, ins []string) error {
	for _, sub := range []string{"results", "images"} {
		if err := os.MkdirAll(filepath.Join(out, sub), 0o755); err != nil {
			return err
		}
	}
	for _, in := range ins {
		for _, sub := range []string{"results", "images"} {
			src := filepath.Join(in, sub)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if err := copyTree(src, filepath.Join(out, sub)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Zip packages index.html + images/ into a single archive for export.
func Zip(root, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	add := func(abs, name string) error {
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := add(filepath.Join(root, "index.html"), "index.html"); err != nil {
		return err
	}
	imgs := artifact.ImagesDir(root)
	return filepath.WalkDir(imgs, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		return add(p, filepath.ToSlash(rel))
	})
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// --- page model ---

type pageData struct {
	Total, Passed, Failed, Skipped, Crashed int
	Categories                              []categoryData
}

type categoryData struct {
	Name string
	Subs []subData
}

type subData struct {
	Name  string
	Tests []testData
}

type testData struct {
	Anchor      string
	Name        string
	Description string
	Wire        string
	Status      string
	StatusClass string
	Duration    string
	CodeHTML    template.HTML
	Shots       []shotData
	Diagnostics template.HTML
	HasDiag     bool
}

type shotData struct {
	Label string
	PNG   string
	Text  template.HTML
}

func buildPage(results []artifact.Result) pageData {
	var p pageData
	catIdx := map[string]int{}
	subIdx := map[string]int{} // key "cat\x00sub"

	for _, r := range results {
		p.Total++
		switch r.Status {
		case artifact.StatusPassed:
			p.Passed++
		case artifact.StatusFailed:
			p.Failed++
		case artifact.StatusSkipped:
			p.Skipped++
		case artifact.StatusCrashed:
			p.Crashed++
		}

		cat := orDash(r.Category)
		sub := r.Subcategory
		if sub == "" {
			sub = cat
		}
		ci, ok := catIdx[cat]
		if !ok {
			ci = len(p.Categories)
			catIdx[cat] = ci
			p.Categories = append(p.Categories, categoryData{Name: cat})
		}
		key := cat + "\x00" + sub
		si, ok := subIdx[key]
		if !ok {
			si = len(p.Categories[ci].Subs)
			subIdx[key] = si
			p.Categories[ci].Subs = append(p.Categories[ci].Subs, subData{Name: sub})
		}

		shots := make([]shotData, 0, len(r.Screenshots))
		for _, s := range r.Screenshots {
			text := template.HTML("")
			if s.PNGPath == "" {
				text = ansiToHTML(s.Text)
			}
			shots = append(shots, shotData{Label: s.Label, PNG: filepath.ToSlash(s.PNGPath), Text: text})
		}

		p.Categories[ci].Subs[si].Tests = append(p.Categories[ci].Subs[si].Tests, testData{
			Anchor:      r.Slug(),
			Name:        r.Name,
			Description: r.Description,
			Wire:        r.Wire,
			Status:      string(r.Status),
			StatusClass: "s-" + string(r.Status),
			Duration:    fmt.Sprintf("%dms", r.DurationMS),
			CodeHTML:    highlight(r.Source),
			Shots:       shots,
			Diagnostics: ansiToHTML(r.Diagnostics),
			HasDiag:     strings.TrimSpace(r.Diagnostics) != "",
		})
	}

	sort.Slice(p.Categories, func(i, j int) bool { return p.Categories[i].Name < p.Categories[j].Name })
	for ci := range p.Categories {
		sort.Slice(p.Categories[ci].Subs, func(i, j int) bool {
			return p.Categories[ci].Subs[i].Name < p.Categories[ci].Subs[j].Name
		})
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "uncategorized"
	}
	return s
}

// codeFormatter emits inline-styled spans (no external stylesheet, no
// standalone <html> document) so the highlighted source drops cleanly into a
// card instead of carrying its own page chrome.
var codeFormatter = html.New(html.WithClasses(false), html.TabWidth(4))
var codeStyle = func() *chroma.Style {
	if s := styles.Get("github-dark"); s != nil {
		return s
	}
	return styles.Fallback
}()

// highlight renders Go source to inline-styled HTML; falls back to a plain <pre>.
func highlight(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	lexer := lexers.Get("go")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	var buf bytes.Buffer
	if err := codeFormatter.Format(&buf, codeStyle, it); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}
