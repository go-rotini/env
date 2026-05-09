package dotenv_test

import (
	"bytes"
	"testing"

	"github.com/go-rotini/env/dotenv"
)

// Fuzz targets for the dotenv package. The contract is:
//
//  1. No panic for any input.
//  2. No infinite loop.
//  3. For round-trip fuzzers: Parse → Marshal → Parse produces the
//     same effective key/value map.
//
// Seed corpora live in dotenv/testdata/fuzz/{TestName}/.

// scannerSeeds covers BOM/CRLF/quoting/escape/expansion forms plus
// known regression cases.
var scannerSeeds = [][]byte{
	[]byte(""),
	[]byte("\n"),
	[]byte("FOO=bar\n"),
	[]byte("export FOO=bar\n"),
	[]byte("FOO: bar\n"),
	[]byte("FOO=\"line1\\nline2\"\n"),
	[]byte("FOO='single $literal'\n"),
	[]byte("FOO=`backtick`\n"),
	[]byte("FOO=value # trailing comment\n"),
	[]byte("# top comment\nFOO=bar\n"),
	[]byte("FOO=\"multi\nline\"\n"),
	{0xEF, 0xBB, 0xBF, 'F', 'O', 'O', '=', 'b', 'a', 'r', '\n'}, // UTF-8 BOM
	[]byte("FOO=line1\r\nBAR=line2\r"),                          // mixed line endings
	[]byte("FOO=\\$NOT_EXPANDED\n"),
	[]byte("FOO=${BAR:-default}\n"),
	[]byte("FOO=${BAR:?required}\n"),
	[]byte("FOO=${A:-${B:-${C}}}\n"), // nested defaults
	[]byte("\xff\xfe\xfd"),           // garbage bytes
}

// FuzzScanner exercises the parser with default options. Any panic
// in the tokenizer or AST construction shows up here.
func FuzzScanner(f *testing.F) {
	for _, s := range scannerSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dotenv.Parse(data)
	})
}

// FuzzParser exercises the parser with all dialect-relaxing options
// enabled simultaneously. Catches interaction bugs between options
// the default-options FuzzScanner won't reach.
func FuzzParser(f *testing.F) {
	for _, s := range scannerSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dotenv.Parse(data,
			dotenv.WithBackticks(),
			dotenv.WithRelaxedNames(),
		)
	})
}

// FuzzExpand exercises the expansion pass. Inputs that hit the
// max-depth cap, nested defaults, assertion forms, and escape
// suppression all flow through here.
func FuzzExpand(f *testing.F) {
	for _, s := range [][]byte{
		[]byte("A=hello\nB=$A\n"),
		[]byte("A=hello\nB=${A}_world\n"),
		[]byte("A=hello\nB=\"prefix-${A}-suffix\"\n"),
		[]byte("A=\nB=${A:-fallback}\n"),
		[]byte("A=set\nB=${A:?required}\n"),
		[]byte("A=${MISSING:-${ALSO_MISSING:-final}}\n"),
		[]byte("A=${B:-${C:-${D:-${E:-final}}}}\n"),
		[]byte("A=\\${LITERAL}\n"),
		[]byte("A=$\nB=$.\nC=${UNCLOSED\n"),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dotenv.Parse(data, dotenv.WithExpand())
		_, _ = dotenv.Parse(data, dotenv.WithExpand(), dotenv.WithStrictExpansion())
		_, _ = dotenv.Parse(data, dotenv.WithExpand(), dotenv.WithMaxExpansionDepth(4))
	})
}

// FuzzRoundTrip is the round-trip property fuzzer. Parse → Marshal
// → Parse must produce the same effective map for any input that
// successfully parsed in the first place.
func FuzzRoundTrip(f *testing.F) {
	for _, s := range [][]byte{
		[]byte("FOO=bar\nBAZ=qux\n"),
		[]byte("# comment\nFOO=bar\n\n"),
		[]byte("export FOO=bar\nBAR='literal'\nBAZ=\"escaped\\n\"\n"),
		[]byte("FOO=\"line1\nline2\"\n"),
		[]byte("FOO=value # trailing\n"),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := dotenv.Parse(data)
		if err != nil {
			return // ill-formed inputs are out of scope for round-trip.
		}
		marshaled, err := first.Marshal()
		if err != nil {
			t.Fatalf("Marshal failed for previously-parsed input: %v", err)
		}
		second, err := dotenv.Parse(marshaled)
		if err != nil {
			t.Fatalf("re-parse of Marshal output failed: %v\n--- bytes ---\n%s",
				err, string(marshaled))
		}
		firstMap := first.ToMap()
		secondMap := second.ToMap()
		if !equalMaps(firstMap, secondMap) {
			t.Fatalf("round-trip diverged:\n  first=%v\n second=%v",
				firstMap, secondMap)
		}
		// Marshal output must itself be byte-stable for unmutated entries.
		if !bytes.Equal(data, marshaled) {
			// Not a hard failure — quoting normalization can legitimately
			// change byte representation — but the second parse must
			// re-marshal stably.
			again, err := second.Marshal()
			if err != nil {
				t.Fatalf("second Marshal failed: %v", err)
			}
			if !bytes.Equal(marshaled, again) {
				t.Fatalf("second round-trip not byte-stable:\n  once=%q\n twice=%q",
					string(marshaled), string(again))
			}
		}
	})
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}
