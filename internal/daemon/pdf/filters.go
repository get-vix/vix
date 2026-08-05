package pdf

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
)

// flateDecode inflates zlib/deflate data. It first tries a proper zlib stream
// (the common case) and falls back to raw DEFLATE, tolerating trailing garbage.
func flateDecode(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		if out, err := io.ReadAll(zr); err == nil {
			return out, nil
		} else if len(out) > 0 {
			return out, nil // salvage partial output from a truncated stream
		}
	}
	// Fall back to raw DEFLATE (some producers omit the zlib header).
	fr := flate.NewReader(bytes.NewReader(data))
	out, err := io.ReadAll(fr)
	if err != nil && len(out) == 0 {
		// Try skipping a 2-byte zlib header manually.
		if len(data) > 2 {
			fr2 := flate.NewReader(bytes.NewReader(data[2:]))
			if out2, err2 := io.ReadAll(fr2); err2 == nil || len(out2) > 0 {
				return out2, nil
			}
		}
		return nil, fmt.Errorf("pdf: flate decode: %w", err)
	}
	return out, nil
}

// applyPredictor reverses PNG (predictor >= 10) or TIFF (predictor == 2)
// prediction applied before compression, as used by xref and content streams.
func applyPredictor(data []byte, predictor, colors, bpc, columns int) ([]byte, error) {
	if predictor < 2 {
		return data, nil
	}
	if colors <= 0 {
		colors = 1
	}
	if bpc <= 0 {
		bpc = 8
	}
	if columns <= 0 {
		columns = 1
	}
	bytesPerPixel := (colors*bpc + 7) / 8
	rowLen := (colors*bpc*columns + 7) / 8
	if rowLen <= 0 {
		return data, nil
	}

	if predictor == 2 {
		// TIFF Predictor 2: horizontal differencing (8-bit components only here).
		if bpc != 8 {
			return data, nil
		}
		out := make([]byte, len(data))
		copy(out, data)
		for r := 0; r+rowLen <= len(out); r += rowLen {
			row := out[r : r+rowLen]
			for i := bytesPerPixel; i < len(row); i++ {
				row[i] += row[i-bytesPerPixel]
			}
		}
		return out, nil
	}

	// PNG predictors: each row is prefixed with a filter-type byte.
	stride := rowLen + 1
	var out bytes.Buffer
	prev := make([]byte, rowLen)
	for r := 0; r+stride <= len(data); r += stride {
		ft := data[r]
		row := make([]byte, rowLen)
		copy(row, data[r+1:r+stride])
		for i := 0; i < rowLen; i++ {
			var a, b, c int // left, up, up-left
			if i >= bytesPerPixel {
				a = int(row[i-bytesPerPixel])
			}
			b = int(prev[i])
			if i >= bytesPerPixel {
				c = int(prev[i-bytesPerPixel])
			}
			switch ft {
			case 0: // None
			case 1: // Sub
				row[i] += byte(a)
			case 2: // Up
				row[i] += byte(b)
			case 3: // Average
				row[i] += byte((a + b) / 2)
			case 4: // Paeth
				row[i] += byte(paeth(a, b, c))
			}
		}
		out.Write(row)
		prev = row
	}
	return out.Bytes(), nil
}

func paeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := abs(p-a), abs(p-b), abs(p-c)
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// asciiHexDecode reverses the ASCIIHexDecode filter.
func asciiHexDecode(data []byte) ([]byte, error) {
	var out []byte
	hi := -1
	for _, b := range data {
		if b == '>' {
			break
		}
		if isWhite(b) {
			continue
		}
		v := hexVal(b)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return out, nil
}

// ascii85Decode reverses the ASCII85Decode filter.
func ascii85Decode(data []byte) ([]byte, error) {
	var out []byte
	var group [5]byte
	n := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '~' {
			break
		}
		if isWhite(c) {
			continue
		}
		if c == 'z' && n == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c < '!' || c > 'u' {
			continue
		}
		group[n] = c - '!'
		n++
		if n == 5 {
			var val uint32
			for _, g := range group {
				val = val*85 + uint32(g)
			}
			out = append(out, byte(val>>24), byte(val>>16), byte(val>>8), byte(val))
			n = 0
		}
	}
	if n > 0 {
		for i := n; i < 5; i++ {
			group[i] = 84
		}
		var val uint32
		for _, g := range group {
			val = val*85 + uint32(g)
		}
		b := []byte{byte(val >> 24), byte(val >> 16), byte(val >> 8), byte(val)}
		out = append(out, b[:n-1]...)
	}
	return out, nil
}

// runLengthDecode reverses the RunLengthDecode filter.
func runLengthDecode(data []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		length := data[i]
		i++
		switch {
		case length == 128:
			return out, nil // EOD
		case length < 128:
			n := int(length) + 1
			if i+n > len(data) {
				n = len(data) - i
			}
			out = append(out, data[i:i+n]...)
			i += n
		default:
			n := 257 - int(length)
			if i >= len(data) {
				return out, nil
			}
			b := data[i]
			i++
			for k := 0; k < n; k++ {
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// lzwDecode reverses the LZWDecode filter (variable-width codes, early change).
func lzwDecode(data []byte, earlyChange int) ([]byte, error) {
	const (
		clearCode = 256
		eodCode   = 257
	)
	if earlyChange != 0 {
		earlyChange = 1
	}
	var out []byte
	dict := make([][]byte, 258, 4096)
	for i := 0; i < 256; i++ {
		dict[i] = []byte{byte(i)}
	}
	codeWidth := 9
	var prev []byte

	bitPos := 0
	readCode := func() (int, bool) {
		if (bitPos+codeWidth+7)/8 > len(data) {
			return 0, false
		}
		code := 0
		for i := 0; i < codeWidth; i++ {
			bytePos := bitPos / 8
			bit := 7 - (bitPos % 8)
			code = (code << 1) | int((data[bytePos]>>bit)&1)
			bitPos++
		}
		return code, true
	}

	for {
		code, ok := readCode()
		if !ok {
			break
		}
		if code == eodCode {
			break
		}
		if code == clearCode {
			dict = dict[:258]
			codeWidth = 9
			prev = nil
			continue
		}
		var entry []byte
		if code < len(dict) && dict[code] != nil {
			entry = dict[code]
		} else if prev != nil {
			entry = append(append([]byte{}, prev...), prev[0])
		} else {
			break
		}
		out = append(out, entry...)
		if prev != nil {
			newEntry := append(append([]byte{}, prev...), entry[0])
			dict = append(dict, newEntry)
		}
		prev = entry
		if len(dict)+earlyChange >= (1<<codeWidth) && codeWidth < 12 {
			codeWidth++
		}
	}
	return out, nil
}
