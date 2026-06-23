//go:build cgo

package daemon

import (
	"fmt"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

func TestDebugTree(t *testing.T) {
	src := []byte(`package main

import (
	"fmt"
	"math"
)
`)
	parser := sitter.NewParser()
	parser.SetLanguage(sitter.NewLanguage(tree_sitter_go.Language()))
	tree := parser.Parse(src, nil)
	printNode(tree.RootNode(), src, 0)
	_ = fmt.Sprintf
}

func printNode(node *sitter.Node, src []byte, depth int) {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	text := ""
	if node.ChildCount() == 0 {
		text = " = " + string(node.Utf8Text(src))
	}
	fmt.Printf("%s%s%s\n", indent, node.Kind(), text)
	for i := uint(0); i < node.ChildCount(); i++ {
		printNode(node.Child(i), src, depth+1)
	}
}
