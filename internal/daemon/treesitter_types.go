package daemon

// srcSegment maps a contiguous run of bytes in the minified output back to the
// original source. Each emitted token produces one segment: the byte range
// [outStart, outStart+length) in the minified string corresponds, byte-for-byte,
// to [srcStart, srcStart+length) in the original source, because a token's text
// is a verbatim copy of the source bytes it came from. The gaps between segments
// are synthetic — inserted separators (";", "\n", indentation) and
// merge-prevention spaces that exist nowhere in the source.
//
// This type lives in a cgo-free file so non-cgo builds (e.g. Windows without a
// C compiler) can still compile the vfs code that consumes the position map,
// even though the map itself is only populated by the cgo tree-sitter minifier.
type srcSegment struct {
	outStart int // byte offset in the minified output where the token begins
	length   int // byte length of the token (== byteEnd-byteStart in source)
	srcStart int // byte offset in the original source where the token begins
}
