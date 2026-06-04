package daemon

import (
	"fmt"
	goparser "go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_swift "github.com/gridlhq-dev/tree-sitter-swift/bindings/go"
	tree_sitter_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tree_sitter_bash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tree_sitter_csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tree_sitter_cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tree_sitter_css "github.com/tree-sitter/tree-sitter-css/bindings/go"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_html "github.com/tree-sitter/tree-sitter-html/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_json "github.com/tree-sitter/tree-sitter-json/bindings/go"
	tree_sitter_php "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/get-vix/vix/internal/daemon/brain"
)

// treeSitterLanguages maps language names (as defined in settings.json) to Tree-sitter
// language constructors. This is the only place tree-sitter grammars are registered.
var treeSitterLanguages map[string]func() *sitter.Language

func init() {
	goLang := sitter.NewLanguage(tree_sitter_go.Language())
	jsLang := sitter.NewLanguage(tree_sitter_javascript.Language())
	pyLang := sitter.NewLanguage(tree_sitter_python.Language())
	rsLang := sitter.NewLanguage(tree_sitter_rust.Language())
	cLang := sitter.NewLanguage(tree_sitter_c.Language())
	cppLang := sitter.NewLanguage(tree_sitter_cpp.Language())
	javaLang := sitter.NewLanguage(tree_sitter_java.Language())
	swiftLang := sitter.NewLanguage(tree_sitter_swift.Language())
	kotlinLang := sitter.NewLanguage(tree_sitter_kotlin.Language())
	rubyLang := sitter.NewLanguage(tree_sitter_ruby.Language())
	phpLang := sitter.NewLanguage(tree_sitter_php.LanguagePHP())
	bashLang := sitter.NewLanguage(tree_sitter_bash.Language())
	csharpLang := sitter.NewLanguage(tree_sitter_csharp.Language())
	jsonLang := sitter.NewLanguage(tree_sitter_json.Language())
	htmlLang := sitter.NewLanguage(tree_sitter_html.Language())
	cssLang := sitter.NewLanguage(tree_sitter_css.Language())
	tsLang := sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
	tsxLang := sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())

	treeSitterLanguages = map[string]func() *sitter.Language{
		"go":         func() *sitter.Language { return goLang },
		"javascript": func() *sitter.Language { return jsLang },
		"typescript": func() *sitter.Language { return tsLang },
		"tsx":        func() *sitter.Language { return tsxLang },
		"python":     func() *sitter.Language { return pyLang },
		"rust":       func() *sitter.Language { return rsLang },
		"c":          func() *sitter.Language { return cLang },
		"cpp":        func() *sitter.Language { return cppLang },
		"java":       func() *sitter.Language { return javaLang },
		"swift":      func() *sitter.Language { return swiftLang },
		"kotlin":     func() *sitter.Language { return kotlinLang },
		"csharp":     func() *sitter.Language { return csharpLang },
		"ruby":       func() *sitter.Language { return rubyLang },
		"php":        func() *sitter.Language { return phpLang },
		"shell":      func() *sitter.Language { return bashLang },
		"json":       func() *sitter.Language { return jsonLang },
		"html":       func() *sitter.Language { return htmlLang },
		"css":        func() *sitter.Language { return cssLang },
	}
}

// newParserForFile creates a new Tree-sitter parser for the given file.
// It resolves the file extension to a language name via the settings.json-based
// language map, then looks up the corresponding tree-sitter grammar.
// Each call returns a fresh parser, safe for concurrent use across goroutines.
func newParserForFile(filePath string) *sitter.Parser {
	ext := strings.ToLower(filepath.Ext(filePath))
	lang := brain.LanguageForExt(ext)
	if lang == "" {
		return nil
	}
	langFn, ok := treeSitterLanguages[lang]
	if !ok {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(langFn())
	return p
}

// minifyWithTreeSitter uses Tree-sitter to parse and minify source code.
// When keepComments is true, comments are preserved in the output.
func minifyWithTreeSitter(content string, filePath string, keepComments bool) (string, error) {
	parser := newParserForFile(filePath)
	if parser == nil {
		return "", nil
	}
	defer parser.Close()

	tree := parser.Parse([]byte(content), nil)
	if tree == nil || tree.RootNode() == nil {
		return "", nil
	}
	defer tree.Close()

	source := []byte(content)
	var tokens []minifyToken
	collectLeaves(tree.RootNode(), source, &tokens, keepComments)

	// Ensure tokens before comments get newline separators so that
	// language-specific semicolon insertion sees the line boundary correctly.
	annotateComments(tokens)

	// Language-specific annotation
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		annotateGo(tokens)
	case ".swift":
		annotateSwift(tokens)
	case ".kt", ".kts":
		annotateKotlin(tokens)
	case ".py":
		annotatePython(tokens)
	case ".sh", ".bash":
		annotateBash(tokens, source)
	case ".rb":
		annotateRuby(tokens)
	case ".html":
		annotateHTML(tokens)
	}
	// C/C++ preproc_include newlines are handled inline by collectLeaves

	// Preserve explicit semicolons present in the source between tokens.
	// Tree-sitter grammars for some languages (Swift, Go, Kotlin, Ruby, Bash)
	// consume `;` as anonymous punctuation rather than emitting it as a leaf,
	// so the line-based annotators can miss them when the input is already
	// minified (all statements on one line). Without this pass, minification
	// of minified input is not idempotent — causing edit-tool old_string
	// matches to fail on a second edit pass.
	if usesSemicolonSeparator(ext) {
		preserveSemicolons(tokens, source, ext)
	}

	out := minifyTokens(tokens)

	// Validation: re-parse the minified output and reject if minification
	// introduced new syntax errors that weren't in the input. This catches
	// separator/spacing bugs (like Swift's `?.` → `? .`) before invalid
	// output reaches disk and breaks downstream formatters.
	if out != "" && !tree.RootNode().HasError() {
		if err := revalidateMinified(ext, out, parser); err != nil {
			return "", fmt.Errorf("minifier produced invalid syntax for %s: %w", filePath, err)
		}
	}

	return out, nil
}

// usesSemicolonSeparator reports whether the language uses `;` as an
// explicit statement separator that may appear in the source between tokens.
// Enabling preserveSemicolons for these languages ensures idempotency when
// the minifier re-processes already-minified single-line input.
func usesSemicolonSeparator(ext string) bool {
	switch ext {
	case ".swift", ".kt", ".kts", ".go", ".rb", ".sh", ".bash",
		".js", ".ts", ".java", ".c", ".cpp", ".cs", ".rs", ".php", ".css":
		return true
	}
	return false
}

// preserveSemicolons inspects the source bytes between each adjacent pair
// of tokens and sets the prior token's separator to ";" when a semicolon
// appears between them and no newline/semicolon separator was already set
// by a language-specific annotator. This preserves statement boundaries
// that would otherwise be lost when tree-sitter's grammar consumes `;`
// as anonymous punctuation instead of emitting it as a leaf token.
func preserveSemicolons(tokens []minifyToken, source []byte, ext string) {
	// tree-sitter-kotlin's grammar requires literal newlines in specific
	// contexts (e.g. after `;` in enum bodies, before `}` that closes a
	// class body). Since we can't easily enumerate those contexts from
	// outside the grammar, preserve every source newline for Kotlin —
	// this makes output slightly longer but guarantees parseability and
	// idempotency.
	preserveAllNLs := ext == ".kt" || ext == ".kts"
	for i := 0; i < len(tokens)-1; i++ {
		start := tokens[i].byteEnd
		end := tokens[i+1].byteStart
		if end <= start || int(end) > len(source) {
			continue
		}
		gap := source[start:end]
		hasSemi := false
		hasNL := false
		for _, b := range gap {
			if b == ';' {
				hasSemi = true
			} else if b == '\n' {
				hasNL = true
			}
		}
		if preserveAllNLs && hasNL {
			if hasSemi {
				tokens[i].separator = ";\n"
			} else if tokens[i].separator == "" || tokens[i].separator == ";" {
				// Upgrade plain separator to include newline.
				tokens[i].separator = tokens[i].separator + "\n"
			}
			continue
		}
		if hasSemi && tokens[i].separator != ";" && tokens[i].separator != ";\n" {
			tokens[i].separator = ";"
		}
	}
}

// revalidateMinified re-parses minified output and returns an error if the
// minifier produced syntactically invalid code. Per-language dispatch:
//   - .go uses go/parser (stdlib) — authoritative, and avoids tree-sitter-go's
//     false positive on grouped `const(A=1;B;C)` declarations that Go accepts.
//   - everything else re-parses with the same tree-sitter grammar used to
//     tokenize the input and flags any ERROR nodes.
func revalidateMinified(ext, out string, tsParser *sitter.Parser) error {
	if ext == ".go" {
		_, err := goparser.ParseFile(gotoken.NewFileSet(), "", out, goparser.SkipObjectResolution)
		return err
	}
	verifyTree := tsParser.Parse([]byte(out), nil)
	if verifyTree == nil {
		return nil
	}
	defer verifyTree.Close()
	root := verifyTree.RootNode()
	if root == nil || !root.HasError() {
		return nil
	}
	// Locate the first ERROR / missing node and include a snippet of the
	// minified output around it so callers can see what the minifier broke.
	errNode := findFirstErrorNode(root)
	if errNode == nil {
		return fmt.Errorf("tree-sitter detected syntax errors")
	}
	startByte := int(errNode.StartByte())
	endByte := int(errNode.EndByte())
	ctxStart := startByte - 40
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := endByte + 40
	if ctxEnd > len(out) {
		ctxEnd = len(out)
	}
	snippet := out[ctxStart:ctxEnd]
	pos := errNode.StartPosition()
	return fmt.Errorf("tree-sitter detected syntax errors at line %d col %d (byte %d-%d): %q (near: %q)",
		pos.Row+1, pos.Column+1, startByte, endByte, out[startByte:endByte], snippet)
}

// findFirstErrorNode walks the tree and returns the first ERROR or MISSING
// node encountered in document order, or nil if none exists.
func findFirstErrorNode(n *sitter.Node) *sitter.Node {
	if n == nil {
		return nil
	}
	if n.IsError() || n.IsMissing() {
		return n
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		if child.HasError() || child.IsError() || child.IsMissing() {
			if found := findFirstErrorNode(child); found != nil {
				return found
			}
		}
	}
	return nil
}

// annotateComments ensures that comment tokens are properly separated from
// surrounding code. For each comment token, the previous code token gets a
// newline separator so that language-specific semicolons are inserted correctly.
// Comment tokens also set their own line number to be different from the next
// token so annotators see a line boundary.
func annotateComments(tokens []minifyToken) {
	for i := range tokens {
		if !tokens[i].isComment {
			continue
		}
		// Give the previous token a newline so annotators see a line change.
		// Skip if the previous token is an explicit semicolon — the `;` already
		// provides statement separation, and injecting an extra `\n` would make
		// re-minifying already-minified input non-idempotent (disk has `;//`,
		// re-minify would emit `;\n//`).
		if i > 0 && tokens[i-1].separator == "" && tokens[i-1].text != ";" {
			tokens[i-1].separator = "\n"
		}
		// Ensure the comment's line number differs from the next code token
		// so annotators don't merge them.
		if i+1 < len(tokens) {
			tokens[i].line = tokens[i+1].line - 1
		}
	}
}

// minifyToken represents a leaf token collected from the tree-sitter AST.
type minifyToken struct {
	text           string
	line           uint   // source line number
	col            uint   // source column (byte offset from start of line)
	byteStart      uint   // byte offset in source where this token starts
	byteEnd        uint   // byte offset in source where this token ends
	separator      string // optional separator to emit after this token (e.g. ";", "\n")
	spaceSeparated bool   // true for tokens inside %w[] / %i[] where whitespace separates elements
	isComment      bool   // true for comment tokens (line/block comments)
	parentKind     string // tree-sitter node type of the parent node (e.g. "ternary_expression")
}

// collectLeaves walks the AST and collects leaf tokens.
// When keepComments is false, comment nodes are skipped.
func collectLeaves(node *sitter.Node, source []byte, tokens *[]minifyToken, keepComments bool) {
	collectLeavesInner(node, source, tokens, false, keepComments, "")
}

func collectLeavesInner(node *sitter.Node, source []byte, tokens *[]minifyToken, spaceSeparated bool, keepComments bool, parentKind string) {
	if node == nil {
		return
	}

	nodeType := node.Kind()

	// Skip whitespace nodes
	switch nodeType {
	case "\n", "\t", " ":
		return
	}

	// Handle comment nodes
	switch nodeType {
	case "comment", "line_comment", "block_comment", "multiline_comment":
		if !keepComments {
			return
		}
		// Emit the full comment text as a single token.
		// Comments always get a newline after them: line comments (// ...) would
		// otherwise comment out following code on the same line, and block comments
		// benefit from visual separation.
		text := node.Utf8Text(source)
		if text != "" {
			tok := minifyToken{
				text:      text,
				line:      uint(node.StartPosition().Row),
				col:       uint(node.StartPosition().Column),
				byteStart: uint(node.StartByte()),
				byteEnd:   uint(node.EndByte()),
				separator: "\n",
				isComment: true,
			}
			*tokens = append(*tokens, tok)
		}
		return
	}

	// Atomic nodes: some tree-sitter grammars (e.g. CSS) have nodes where the numeric
	// part is implicit text not represented as a child (e.g. color_value "#2563eb" has
	// child "#" but "2563eb" is only in the parent's text). Emit these as single tokens.
	if isAtomicNode(nodeType) {
		text := node.Utf8Text(source)
		if text != "" {
			*tokens = append(*tokens, minifyToken{
				text:           text,
				line:           uint(node.StartPosition().Row),
				col:            uint(node.StartPosition().Column),
				byteStart:      uint(node.StartByte()),
				byteEnd:        uint(node.EndByte()),
				spaceSeparated: spaceSeparated,
			})
		}
		return
	}

	// Leaf node: collect it
	if node.ChildCount() == 0 {
		text := node.Utf8Text(source)
		if text != "" && text != "\n" && text != "\t" {
			*tokens = append(*tokens, minifyToken{
				text:           text,
				line:           uint(node.StartPosition().Row),
				col:            uint(node.StartPosition().Column),
				byteStart:      uint(node.StartByte()),
				byteEnd:        uint(node.EndByte()),
				spaceSeparated: spaceSeparated,
				parentKind:     parentKind,
			})
		}
		return
	}

	// HTML doctype: tree-sitter omits the doctype name (e.g. "html") from children,
	// so emit the entire raw text as a single token instead of recursing.
	if nodeType == "doctype" {
		text := node.Utf8Text(source)
		if text != "" {
			*tokens = append(*tokens, minifyToken{
				text:      text,
				line:      uint(node.StartPosition().Row),
				col:       uint(node.StartPosition().Column),
				byteStart: uint(node.StartByte()),
				byteEnd:   uint(node.EndByte()),
			})
		}
		return
	}

	// Ruby %w[] / %i[]: elements are whitespace-separated
	isSpaceSep := nodeType == "string_array" || nodeType == "symbol_array"

	// Recurse into children
	for i := uint(0); i < node.ChildCount(); i++ {
		collectLeavesInner(node.Child(i), source, tokens, spaceSeparated || isSpaceSep, keepComments, nodeType)
	}

	// C/C++: add newline after preprocessor directives.
	// These must occupy their own line — without newlines, `#define FOO 1#define BAR 2`
	// is invalid C/C++ because the preprocessor treats everything until EOL as part
	// of the directive.
	switch nodeType {
	case "preproc_include", "preproc_def", "preproc_function_def", "preproc_call",
		"preproc_ifdef", "preproc_ifndef", "preproc_if", "preproc_elif", "preproc_else",
		"preproc_endif":
		if len(*tokens) > 0 {
			(*tokens)[len(*tokens)-1].separator = "\n"
		}
	}
}

// annotateGo adds semicolons at line boundaries per Go's automatic semicolon insertion rule.
func annotateGo(tokens []minifyToken) {
	for i := 0; i < len(tokens)-1; i++ {
		if tokens[i].isComment {
			continue
		}
		// Find the next non-comment token.
		j := i + 1
		for j < len(tokens) && tokens[j].isComment {
			j++
		}
		if j >= len(tokens) {
			break
		}
		curr := tokens[i]
		next := tokens[j]
		if next.line > curr.line && goSemicolonTrigger(curr.text) && !isClosingToken(next.text) {
			tokens[i].separator = ";"
		}
	}
}

// annotateSwift adds semicolons at line boundaries for Swift.
// Swift uses newlines as statement separators, so when minifying to one line
// we must insert explicit semicolons — except before continuation tokens.
// It also adds spaces around operators that precede '.' (implicit member access)
// because Swift requires symmetric whitespace around operators.
func annotateSwift(tokens []minifyToken) {
	for i := 0; i < len(tokens)-1; i++ {
		curr := tokens[i]
		next := tokens[i+1]
		// Never overwrite a comment's newline separator with ';' — a Swift
		// line comment (`//` or `///`) extends to end-of-line, so placing
		// ';' after it would make the ';' part of the comment text in the
		// one-line minified output.
		if curr.isComment {
			continue
		}
		if next.line > curr.line && swiftSemicolonTrigger(curr.text) && !swiftContinuationToken(next.text) {
			tokens[i].separator = ";"
		}
	}

	// Add spaces around ternary operator tokens (? and :) that belong to a
	// ternary_expression. Swift requires whitespace around these; without it
	// `x!=nil?"yes":"no"` is parsed as optional chaining rather than a ternary.
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok.parentKind == "ternary_expression" && (tok.text == "?" || tok.text == ":") {
			// Space before: set separator on previous token
			if i > 0 && tokens[i-1].separator == "" {
				tokens[i-1].separator = " "
			}
			// Space after: set separator on this token
			if tokens[i].separator == "" {
				tokens[i].separator = " "
			}
		}
	}

	// Add spaces around operators preceding '.' (e.g. status = .running, x == .done).
	// Skip '?' and '!' because `?.` (optional chaining) and `!.` (forced chaining)
	// are single postfix operators in Swift that must not be split by whitespace.
	for i := 0; i < len(tokens)-2; i++ {
		opTok := tokens[i+1].text
		if opTok == "?" || opTok == "!" {
			continue
		}
		if isOperatorToken(opTok) && tokens[i+2].text == "." {
			if tokens[i].separator == "" {
				tokens[i].separator = " "
			}
			tokens[i+1].separator = " "
		}
	}
}

// swiftSemicolonTrigger returns true if a token can end a Swift statement.
func swiftSemicolonTrigger(text string) bool {
	if len(text) == 0 {
		return false
	}
	// Keywords that syntactically require a following expression/block and
	// therefore never terminate a statement, even though they end with a
	// word character. `in` is the closure-signature delimiter (`{ [weak self] in`)
	// and the for-loop separator; `throws`/`rethrows`/`async` appear in
	// function signatures; `try`/`await`/`throw` are expression prefixes.
	switch text {
	case "in", "throws", "rethrows", "async", "try", "await", "throw":
		return false
	}
	lastChar := text[len(text)-1]
	if isWordChar(lastChar) {
		return true
	}
	switch lastChar {
	case ')', ']', '}', '"', '\'':
		return true
	case '>':
		// Closing '>' of a generic type (e.g. `let x: Foo<T>`) ends a
		// declaration when followed by a line break. The rare multiline
		// comparison case (`a > b` split across lines) is protected by
		// swiftContinuationToken handling of the next token.
		return text == ">"
	case '!', '?':
		// Postfix force-unwrap (`x!`) and optional suffix (`Int?`) end
		// expressions/declarations at EOL (e.g. `static let x = Foo()!`
		// or `var x: Int?`). Multiline optional-chaining continuations
		// (`foo?\n.bar`) are protected because '.' is a continuation token.
		return text == "!" || text == "?"
	case '+':
		return text == "++"
	case '-':
		return text == "--"
	}
	return false
}

// swiftContinuationToken returns true for tokens that continue a previous statement
// and should not be preceded by a semicolon.
func swiftContinuationToken(text string) bool {
	switch text {
	case "}", ")", "]", ",", "{", ".", "else", "catch", "where", "in",
		// Property/subscript accessors: peers inside a `{ ... }` block, not
		// separate statements. Inserting `;` between `get { ... }` and
		// `set { ... }` breaks the grammar.
		"get", "set", "willSet", "didSet":
		return true
	}
	return false
}

// annotateKotlin adds semicolons at line boundaries for Kotlin.
// Kotlin uses newlines as statement separators, like Swift.
// It also adds spaces around operators that precede '.' or other operators
// to prevent ambiguous token merging (e.g. List<Task>=x → looks like >=).
func annotateKotlin(tokens []minifyToken) {
	for i := 0; i < len(tokens)-1; i++ {
		curr := tokens[i]
		next := tokens[i+1]
		if next.line > curr.line && kotlinSemicolonTrigger(tokens, i) && !kotlinContinuationToken(next.text) {
			tokens[i].separator = ";"
		}
	}

	// Add spaces around operators preceding '.' (implicit member access)
	// and between consecutive operator tokens to prevent merging (e.g. > = → >=).
	// Skip the `?` + `.` case: Kotlin's `?.` safe-call is a single postfix
	// operator that must not be split by whitespace.
	for i := 0; i < len(tokens)-2; i++ {
		opTok := tokens[i+1].text
		nextTok := tokens[i+2].text
		if opTok == "?" && nextTok == "." {
			continue
		}
		if isOperatorToken(opTok) && (nextTok == "." || isOperatorToken(nextTok)) {
			if tokens[i].separator == "" {
				tokens[i].separator = " "
			}
			tokens[i+1].separator = " "
		}
	}
}

// kotlinSemicolonTrigger checks if the token at index i can end a Kotlin statement.
// Handles the case where tree-sitter may tokenize ++ as two separate + tokens.
func kotlinSemicolonTrigger(tokens []minifyToken, i int) bool {
	text := tokens[i].text
	// Check the standard triggers first.
	if swiftSemicolonTrigger(text) {
		return true
	}
	// Handle ++ / -- split into two tokens: if current is "+" or "-"
	// and the previous token is the same on the same line, treat as ++/--.
	if (text == "+" || text == "-") && i > 0 && tokens[i-1].text == text && tokens[i-1].line == tokens[i].line {
		return true
	}
	return false
}

// annotateBash preserves newlines and source-level spacing for Bash.
// Bash is whitespace-sensitive: spaces separate commands from arguments,
// and newlines separate commands. We copy the exact whitespace that
// appeared in the source between each pair of adjacent tokens, which
// preserves indentation and makes the output idempotent under re-minification.
func annotateBash(tokens []minifyToken, source []byte) {
	for i := 1; i < len(tokens); i++ {
		prev := tokens[i-1]
		curr := tokens[i]
		start := int(prev.byteEnd)
		end := int(curr.byteStart)
		if start < 0 || end > len(source) || start >= end {
			continue
		}
		gap := source[start:end]
		// Keep only whitespace characters (spaces, tabs, newlines) from the
		// gap. Other characters (e.g. comments) are handled elsewhere.
		var b []byte
		for _, c := range gap {
			if c == ' ' || c == '\t' || c == '\n' {
				b = append(b, c)
			}
		}
		// Collapse blank lines: multiple consecutive newlines compress to one.
		if len(b) > 0 {
			collapsed := make([]byte, 0, len(b))
			prevNL := false
			for _, c := range b {
				if c == '\n' {
					if prevNL {
						continue
					}
					prevNL = true
				} else {
					prevNL = false
				}
				collapsed = append(collapsed, c)
			}
			tokens[i-1].separator = string(collapsed)
		}
	}
}

// annotateRuby adds semicolons at line boundaries for Ruby.
// Ruby uses newlines as statement separators but allows semicolons instead.
func annotateRuby(tokens []minifyToken) {
	for i := 0; i < len(tokens)-1; i++ {
		curr := tokens[i]
		next := tokens[i+1]
		// Inside %w[] / %i[]: elements are whitespace-separated. Always
		// emit a space between spaceSeparated tokens, regardless of line
		// boundaries, so re-minification stays idempotent.
		if curr.spaceSeparated && next.spaceSeparated && tokens[i].separator == "" {
			tokens[i].separator = " "
			continue
		}
		if next.line > curr.line {
			if rubySemicolonTrigger(curr.text) && !rubyContinuationToken(next.text) {
				tokens[i].separator = ";"
			}
		}
		// The shift operator `<<` is ambiguous with heredoc syntax in Ruby.
		// `arr<<x` (no space) looks like `arr` followed by a `<<x` heredoc
		// to tree-sitter-ruby, which then treats everything until a line
		// starting with `x` as the heredoc body and produces a parse error.
		// Inserting a space after `<<` disambiguates: `arr<< x` is the
		// shift operator, never a heredoc.
		if curr.text == "<<" && tokens[i].separator == "" && len(next.text) > 0 && isWordChar(next.text[0]) {
			tokens[i].separator = " "
		}
	}
}

// rubySemicolonTrigger returns true if a token can end a Ruby statement.
func rubySemicolonTrigger(text string) bool {
	if len(text) == 0 {
		return false
	}
	lastChar := text[len(text)-1]
	if isWordChar(lastChar) {
		return true
	}
	switch lastChar {
	case ')', ']', '}', '"', '\'':
		return true
	}
	return false
}

// rubyContinuationToken returns true for tokens that continue a previous statement
// and should not be preceded by a semicolon in Ruby.
func rubyContinuationToken(text string) bool {
	switch text {
	case "}", ")", "]", ",", "{", ".", "do",
		"else", "elsif", "ensure", "rescue", "when", "then", "in":
		return true
	}
	return false
}

// annotatePython preserves newlines and indentation for Python.
// Python is whitespace-sensitive: blocks are defined by indentation, newlines end statements.
// We keep the line structure but minimize indentation to 1 space per level.
func annotatePython(tokens []minifyToken) {
	if len(tokens) == 0 {
		return
	}

	// Detect the indent unit (smallest non-zero column of a first-on-line token).
	indentUnit := uint(0)
	for i := 1; i < len(tokens); i++ {
		if tokens[i].line > tokens[i-1].line && tokens[i].col > 0 {
			if indentUnit == 0 || tokens[i].col < indentUnit {
				indentUnit = tokens[i].col
			}
		}
	}
	if indentUnit == 0 {
		indentUnit = 4
	}

	// At line boundaries, emit newline + minimal indentation.
	for i := 1; i < len(tokens); i++ {
		if tokens[i].line > tokens[i-1].line {
			indentLevel := int(tokens[i].col / indentUnit)
			sep := "\n" + strings.Repeat(" ", indentLevel)
			tokens[i-1].separator = sep
		}
	}
}

// kotlinContinuationToken returns true for tokens that continue a previous statement
// and should not be preceded by a semicolon in Kotlin.
func kotlinContinuationToken(text string) bool {
	switch text {
	case "}", ")", "]", ",", "{", ".", "else", "catch", "finally", "where", "in":
		return true
	}
	return false
}

// annotateHTML adds spaces between HTML attributes.
// In HTML, attributes must be separated by whitespace. Without annotation,
// the minifier merges closing quotes with the next attribute name (e.g. "en"lang=).
// We detect where a space is needed by checking whether consecutive tokens on
// the same line had a column gap in the original source.
func annotateHTML(tokens []minifyToken) {
	for i := 0; i < len(tokens)-1; i++ {
		curr := tokens[i]
		next := tokens[i+1]
		if curr.line != next.line {
			continue
		}
		// If there was a gap in the source between the end of curr and start of next,
		// insert a space. This preserves required whitespace (e.g. between attributes)
		// while not adding spaces where tokens were adjacent (e.g. inside "value").
		currEnd := curr.col + uint(len(curr.text))
		if next.col > currEnd {
			tokens[i].separator = " "
		}
	}
}

// minifyTokens joins tokens with minimal spacing.
func minifyTokens(tokens []minifyToken) string {
	if len(tokens) == 0 {
		return ""
	}

	var result strings.Builder
	for i, tok := range tokens {
		if i > 0 {
			prev := tokens[i-1]

			// Emit separator from previous token
			if prev.separator != "" {
				result.WriteString(prev.separator)
			}

			// Word-char spacing: add space only when needed to prevent token merging
			if result.Len() > 0 {
				lastChar := result.String()[result.Len()-1]
				if lastChar != '\n' && isWordChar(lastChar) && isWordChar(tok.text[0]) {
					result.WriteByte(' ')
				} else if isOperatorChar(lastChar) && lastChar != '?' && lastChar != '!' && tok.text[0] == '.' {
					// Prevent ambiguous operator-dot sequences (e.g. Swift's =.foo).
					// '?' and '!' are excluded because `?.` (optional chaining) and
					// `!.` (forced chaining) are single postfix operators in
					// Swift/Kotlin/TS/C# and must not be split by whitespace.
					result.WriteByte(' ')
				}
			}
		}
		result.WriteString(tok.text)
	}

	return strings.TrimSpace(result.String())
}

// isAtomicNode returns true for tree-sitter node types that should be emitted as a
// single token rather than recursed into. This handles grammars (like CSS) where
// numeric parts are implicit text not represented as child nodes.
func isAtomicNode(nodeType string) bool {
	switch nodeType {
	case "integer_value", "float_value", "color_value": // CSS
		return true
	case "format_specifier": // Python f-string format specs (e.g. :>3d)
		return true
	}
	return false
}

// isOperatorToken returns true if the entire token consists of operator characters.
func isOperatorToken(text string) bool {
	if len(text) == 0 {
		return false
	}
	for i := 0; i < len(text); i++ {
		if !isOperatorChar(text[i]) {
			return false
		}
	}
	return true
}

// isOperatorChar returns true for characters that are part of operators.
func isOperatorChar(c byte) bool {
	switch c {
	case '=', '!', '<', '>', '+', '-', '*', '/', '&', '|', '^', '%', '~', '?':
		return true
	}
	return false
}

// isWordChar returns true for characters that are part of identifiers/literals.
func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// goSemicolonTrigger returns true if a token triggers Go's automatic semicolon insertion.
func goSemicolonTrigger(text string) bool {
	if len(text) == 0 {
		return false
	}
	lastChar := text[len(text)-1]
	if isWordChar(lastChar) {
		return true
	}
	switch lastChar {
	case ')', ']', '}', '"', '\'', '`':
		return true
	case '+':
		return text == "++"
	case '-':
		return text == "--"
	}
	return false
}

// isClosingToken returns true for tokens that should not be preceded by a Go semicolon.
func isClosingToken(text string) bool {
	return text == "}" || text == ")" || text == "]" || text == ","
}
