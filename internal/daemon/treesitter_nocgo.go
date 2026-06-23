//go:build !cgo

package daemon

// tree-sitter fallback for builds without cgo.
//
// The tree-sitter grammars and go-tree-sitter are cgo-only (each compiles a C
// parser.c via `import "C"`); there is no pure-Go path. On a build with no C
// compiler available (notably GOOS=windows from a bare checkout, where
// CGO_ENABLED=0 and no mingw is present) those packages are excluded by Go's
// build constraints, so the real minifier in treesitter_cgo.go cannot compile.
//
// This file provides a passthrough: minification is skipped and the file
// content is returned verbatim. It changes behaviour ONLY on cgo-free builds
// (which never worked before — they failed to compile). On cgo builds (every
// Unix dev/CI box today) the real minifier in treesitter_cgo.go applies and
// behaviour is unchanged. The daemon does not yet run on cgo-free platforms
// (Waves 1-3 add the Windows backends), so skipping minification there has no
// runtime effect yet; this exists purely to keep the tree compiling.

func minifyWithTreeSitter(content string, filePath string, keepComments bool) (string, error) {
	return content, nil
}

func minifyWithTreeSitterMapped(content string, filePath string, keepComments bool) (string, []srcSegment, error) {
	return content, nil, nil
}
