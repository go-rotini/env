package env

import (
	"encoding"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// fieldDescriptor describes one resolved leaf field after walking a struct.
// It is the unit consumed by the loader pipeline.
//
// Key already includes any envPrefix accumulation from enclosing structs but
// does NOT include the loader-level WithPrefix; the loader applies its
// prefix at lookup time so that field descriptors can be cached per
// reflect.Type independent of any particular Loader configuration.
type fieldDescriptor struct {
	// Index is the path through nested structs, suitable for
	// [reflect.Value.FieldByIndex].
	Index []int

	// GoName is the exact Go field name (e.g. "Port").
	GoName string

	// GoPath is the dotted Go field path used for diagnostics
	// (e.g. "DB.URL"). Anonymous-embedded fields are promoted, so the
	// path follows the embedded-field access convention.
	GoPath string

	// Key is the resolved env-var name after envPrefix accumulation but
	// before loader-level WithPrefix. The loader prepends WithPrefix at
	// lookup time.
	Key string

	// Description is the free-text help string from the envDesc tag.
	Description string

	// Tag-driven flags / values.
	Required     bool
	NotEmpty     bool
	HasDefault   bool
	DefaultValue string
	Separator    string // empty means use the loader default ","
	KVSeparator  string // empty means use the loader default ":"
	Expand       bool
	FromFile     bool
	Unset        bool
	Secret       bool
	Immutable    bool
	Layout       string // for time.Time
	Base64       bool
	Hex          bool
}

// structInfo is the cached field-collection result for a single struct
// type. Err is non-nil for types whose tags or shape are invalid; the
// loader returns Err on every Load against such a type without re-walking.
type structInfo struct {
	Fields []fieldDescriptor
	Err    error
}

// structInfoCache memoizes the per-type field walk. Keyed by reflect.Type.
var structInfoCache sync.Map

// getStructInfo returns the cached structInfo for t, walking and storing
// it on first request. Concurrent callers race to populate the cache; the
// loser's walk is discarded and both observe the winner's value.
func getStructInfo(t reflect.Type) *structInfo {
	if cached, ok := structInfoCache.Load(t); ok {
		info, _ := cached.(*structInfo)
		return info
	}
	info := collectStructInfo(t)
	actual, _ := structInfoCache.LoadOrStore(t, info)
	stored, _ := actual.(*structInfo)
	return stored
}

func collectStructInfo(t reflect.Type) *structInfo {
	if t.Kind() != reflect.Struct {
		return &structInfo{
			Err: fmt.Errorf("got %s: %w", t, ErrNotStruct),
		}
	}
	w := walker{
		seen:           make(map[string]string),
		tagName:        "env",
		fieldNameXform: toScreamingSnake,
	}
	w.walk(t, nil, "", "")
	return &structInfo{
		Fields: w.fields,
		Err:    errors.Join(w.errs...),
	}
}

// walker carries the cross-recursion state for collectStructInfo: the
// accumulated descriptor list, the seen-key map for conflict detection,
// and the error list. Pointer-receiver methods are used throughout so the
// recursion mutates the same instance.
//
// tagName and fieldNameXform are configurable; the package-default cache
// path uses "env" and toScreamingSnake. Loaders configured with
// [WithTagName] or [WithFieldNameTransform] construct a walker with
// custom values and bypass the cache.
type walker struct {
	fields         []fieldDescriptor
	seen           map[string]string
	errs           []error
	tagName        string
	fieldNameXform func(string) string
}

// walk recursively walks t, populating w.fields and w.errs.
//
// parentIndex is the FieldByIndex prefix for fields below t.
// parentPrefix is the accumulated envPrefix string.
// parentGoPath is the dotted Go path to t ("" at the root).
func (w *walker) walk(t reflect.Type, parentIndex []int, parentPrefix, parentGoPath string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}

		envTag, hasEnvTag := f.Tag.Lookup(w.tagName)
		if hasEnvTag && envTag == "-" {
			continue
		}

		index := make([]int, 0, len(parentIndex)+1)
		index = append(index, parentIndex...)
		index = append(index, i)

		goPath := f.Name
		if parentGoPath != "" {
			goPath = parentGoPath + "." + f.Name
		}

		envPrefix := f.Tag.Get("envPrefix")
		envDesc := f.Tag.Get("envDesc")

		// Resolve to the underlying struct type for recursion checks,
		// dereferencing any pointer indirection.
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		// Anonymous embed of a non-leaf struct: recurse without adding
		// the embed's name to the Go path (Go promotes the inner fields).
		if f.Anonymous && ft.Kind() == reflect.Struct && !isLeafStruct(ft) {
			w.walk(ft, index, parentPrefix+envPrefix, parentGoPath)
			continue
		}

		// Named struct field with no env tag: recurse if the type is a
		// non-leaf struct. With an env tag the field is treated as a leaf
		// (the user has explicitly opted into a single key for the whole
		// value, and the decoder is expected to handle it).
		if !hasEnvTag && ft.Kind() == reflect.Struct && !isLeafStruct(ft) && f.IsExported() {
			w.walk(ft, index, parentPrefix+envPrefix, goPath)
			continue
		}

		// Skip any unexported anonymous field that wasn't a struct we
		// could recurse into (e.g., embedded interface).
		if !f.IsExported() {
			continue
		}

		tag, err := parseFieldTag(envTag)
		if err != nil {
			w.errs = append(w.errs, fmt.Errorf("field %s: %w", goPath, err))
			continue
		}

		var key string
		if tag.Name != "" {
			key = parentPrefix + tag.Name
		} else {
			key = parentPrefix + w.fieldNameXform(f.Name)
		}

		if existing, dup := w.seen[key]; dup {
			w.errs = append(w.errs, fmt.Errorf(
				"key %q claimed by %s and %s: %w",
				key, existing, goPath, ErrConflictingFields))
			continue
		}
		w.seen[key] = goPath

		w.fields = append(w.fields, fieldDescriptor{
			Index:        index,
			GoName:       f.Name,
			GoPath:       goPath,
			Key:          key,
			Description:  envDesc,
			Required:     tag.Required,
			NotEmpty:     tag.NotEmpty,
			HasDefault:   tag.HasDefault,
			DefaultValue: tag.DefaultValue,
			Separator:    tag.Separator,
			KVSeparator:  tag.KVSeparator,
			Expand:       tag.Expand,
			FromFile:     tag.FromFile,
			Unset:        tag.Unset,
			Secret:       tag.Secret,
			Immutable:    tag.Immutable,
			Layout:       tag.Layout,
			Base64:       tag.Base64,
			Hex:          tag.Hex,
		})
	}
}

// fieldTag is the parsed result of one `env:"..."` tag.
type fieldTag struct {
	Name string // explicit name, "" means "use default name transform"

	Required     bool
	NotEmpty     bool
	HasDefault   bool
	DefaultValue string
	Separator    string
	KVSeparator  string
	Expand       bool
	FromFile     bool
	Unset        bool
	Secret       bool
	Immutable    bool
	Layout       string
	Base64       bool
	Hex          bool
}

// parseFieldTag parses the body of an `env:"..."` tag value.
//
// A tag of "-" elsewhere is checked by the caller; this function is invoked
// only on non-skip tags. An empty raw string yields a zero-valued fieldTag
// (i.e., "use default name, no options"), which is the same shape as a tag
// of `env:""`.
//
// Option values cannot contain commas; the separator-comma is reserved by
// the option list. For free-text help with arbitrary content use the
// envDesc companion tag.
func parseFieldTag(raw string) (fieldTag, error) {
	t := fieldTag{}
	if raw == "" {
		return t, nil
	}

	parts := strings.Split(raw, ",")
	t.Name = parts[0]

	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key, value, hasValue := strings.Cut(p, "=")
		if hasValue {
			switch key {
			case "default":
				t.HasDefault = true
				t.DefaultValue = value
			case "separator":
				t.Separator = value
			case "kvSeparator":
				t.KVSeparator = value
			case "layout":
				t.Layout = value
			default:
				return t, fmt.Errorf("%w: %q", ErrUnknownTagOption, key)
			}
			continue
		}
		switch key {
		case "required":
			t.Required = true
		case "notEmpty":
			t.NotEmpty = true
		case "expand":
			t.Expand = true
		case "fromFile":
			t.FromFile = true
		case "unset":
			t.Unset = true
		case "secret":
			t.Secret = true
		case "immutable":
			t.Immutable = true
		case "base64":
			t.Base64 = true
		case "hex":
			t.Hex = true
		default:
			return t, fmt.Errorf("%w: %q", ErrUnknownTagOption, key)
		}
	}
	return t, nil
}

// toScreamingSnake transforms a Go field name into its SCREAMING_SNAKE_CASE
// env-var equivalent with acronym detection. Examples:
//
//	Port         -> PORT
//	DatabaseURL  -> DATABASE_URL
//	HTTPSPort    -> HTTPS_PORT     (acronym ends before "Port")
//	IPv4Address  -> IPV4_ADDRESS   (single 'v' is part of "IPv4")
//	URLs         -> URLS           (single trailing lower is acronym suffix)
//	camelCase    -> CAMEL_CASE
//
// Boundary rules:
//
//  1. Lowercase or digit followed by uppercase: insert underscore.
//  2. Uppercase followed by uppercase, where the second uppercase is followed
//     by at least two lowercase letters in a row: insert underscore (a real
//     word, not a 1-character suffix of the acronym).
//
// All boundary insertions happen between letters; non-letter / non-digit
// runes (such as existing underscores) pass through unchanged. The result
// is upper-cased as a final step.
func toScreamingSnake(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	var b strings.Builder
	b.Grow(len(name) + 4)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			switch {
			case (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(r):
				b.WriteByte('_')
			case unicode.IsUpper(prev) && unicode.IsUpper(r):
				if i+2 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsLower(runes[i+2]) {
					b.WriteByte('_')
				}
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// builtinLeafStructTypes is the set of well-known struct types treated as
// opaque leaves by the field walker. Most user-facing types in this set
// also implement [encoding.TextUnmarshaler] or [encoding.BinaryUnmarshaler]
// and would be detected by the interface check; they are listed explicitly
// for performance and to cover types like [net.IPNet] and [regexp.Regexp]
// that implement neither.
var builtinLeafStructTypes = map[reflect.Type]struct{}{
	reflect.TypeFor[time.Time]():     {},
	reflect.TypeFor[time.Location](): {},
	reflect.TypeFor[url.URL]():       {},
	reflect.TypeFor[net.IPNet]():     {},
	reflect.TypeFor[regexp.Regexp](): {},
	reflect.TypeFor[big.Int]():       {},
	reflect.TypeFor[big.Float]():     {},
	reflect.TypeFor[big.Rat]():       {},
}

var (
	textUnmarshalerType   = reflect.TypeFor[encoding.TextUnmarshaler]()
	binaryUnmarshalerType = reflect.TypeFor[encoding.BinaryUnmarshaler]()
)

// isLeafStruct reports whether t is a struct type the field walker should
// treat as an opaque leaf rather than recursing into.
//
// A struct is a leaf when:
//  1. It is in the built-in registry of well-known special types.
//  2. *T implements [encoding.TextUnmarshaler] or [encoding.BinaryUnmarshaler].
//
// Checking only *T is sufficient: Go's method-set rules guarantee that if
// T implements an interface (via value receivers), *T does too.
func isLeafStruct(t reflect.Type) bool {
	if _, ok := builtinLeafStructTypes[t]; ok {
		return true
	}
	pt := reflect.PointerTo(t)
	return pt.Implements(textUnmarshalerType) || pt.Implements(binaryUnmarshalerType)
}
