package core

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// EncKind identifies the detected encoding of a config file so the file can be
// re-encoded byte-for-byte identically after a text replacement (强一致保留原编码).
type EncKind int

const (
	EncUTF8     EncKind = iota // UTF-8 without BOM (also covers plain ASCII)
	EncUTF8BOM                 // UTF-8 with BOM
	EncUTF16LE                 // UTF-16 little-endian (BOM preserved)
	EncUTF16BE                 // UTF-16 big-endian (BOM preserved)
	EncGBK                     // ANSI/GBK (simplified Chinese, code page 936)
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// EncodingName returns a human-readable name for logging.
func EncodingName(k EncKind) string {
	switch k {
	case EncUTF8:
		return "UTF-8（无 BOM）"
	case EncUTF8BOM:
		return "UTF-8（带 BOM）"
	case EncUTF16LE:
		return "UTF-16 LE"
	case EncUTF16BE:
		return "UTF-16 BE"
	case EncGBK:
		return "ANSI/GBK"
	}
	return "未知"
}

// DetectEncoding sniffs raw bytes and returns the encoding kind.
func DetectEncoding(raw []byte) EncKind {
	if len(raw) >= 3 && bytes.Equal(raw[:3], utf8BOM) {
		return EncUTF8BOM
	}
	if len(raw) >= 2 && bytes.Equal(raw[:2], utf16LEBOM) {
		return EncUTF16LE
	}
	if len(raw) >= 2 && bytes.Equal(raw[:2], utf16BEBOM) {
		return EncUTF16BE
	}
	if utf8.Valid(raw) {
		return EncUTF8
	}
	return EncGBK
}

// Decode converts raw bytes to a UTF-8 Go string using the detected encoding,
// stripping any BOM.
func Decode(k EncKind, raw []byte) (string, error) {
	switch k {
	case EncUTF8BOM:
		return string(raw[3:]), nil
	case EncUTF8:
		return string(raw), nil
	case EncUTF16LE:
		return decodeUTF16(raw, unicode.LittleEndian)
	case EncUTF16BE:
		return decodeUTF16(raw, unicode.BigEndian)
	case EncGBK:
		return decodeTransform(simplifiedchinese.GBK.NewDecoder(), raw)
	}
	return string(raw), nil
}

// Encode converts a UTF-8 Go string back to bytes using the original encoding
// kind, re-adding the BOM when it was present.
func Encode(k EncKind, s string) ([]byte, error) {
	switch k {
	case EncUTF8BOM:
		buf := make([]byte, 0, len(s)+3)
		buf = append(buf, utf8BOM...)
		return append(buf, s...), nil
	case EncUTF8:
		return []byte(s), nil
	case EncUTF16LE:
		return encodeUTF16(s, unicode.LittleEndian)
	case EncUTF16BE:
		return encodeUTF16(s, unicode.BigEndian)
	case EncGBK:
		return encodeTransform(simplifiedchinese.GBK.NewEncoder(), s)
	}
	return []byte(s), nil
}

func decodeUTF16(raw []byte, endian unicode.Endianness) (string, error) {
	// x/text's IgnoreBOM decoder keeps the BOM as a U+FEFF character in the
	// output, which would corrupt a round-trip (double BOM). Strip the BOM
	// bytes explicitly here; Encode re-adds it so bytes stay identical.
	content := raw
	switch {
	case len(raw) >= 2 && bytes.Equal(raw[:2], utf16LEBOM):
		content = raw[2:]
	case len(raw) >= 2 && bytes.Equal(raw[:2], utf16BEBOM):
		content = raw[2:]
	}
	e := unicode.UTF16(endian, unicode.IgnoreBOM)
	return decodeTransform(e.NewDecoder(), content)
}

func encodeUTF16(s string, endian unicode.Endianness) ([]byte, error) {
	var buf bytes.Buffer
	if endian == unicode.LittleEndian {
		buf.Write(utf16LEBOM)
	} else {
		buf.Write(utf16BEBOM)
	}
	e := unicode.UTF16(endian, unicode.IgnoreBOM)
	encBytes, err := e.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return nil, err
	}
	buf.Write(encBytes)
	return buf.Bytes(), nil
}

func decodeTransform(dec *encoding.Decoder, raw []byte) (string, error) {
	out, _, err := transform.Bytes(dec, raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func encodeTransform(enc *encoding.Encoder, s string) ([]byte, error) {
	return enc.Bytes([]byte(s))
}
