package tree_sitter_swift

// #cgo CFLAGS: -std=c11 -fPIC -I${SRCDIR}/../../src
// #include "../../src/parser.c"
// #undef TOKEN_COUNT
// #include "../../src/scanner.c"
import "C"

import "unsafe"

func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_swift())
}
