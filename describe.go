package env

import (
	"fmt"
	"io"
	"reflect"
	"strings"
)

// FieldDoc describes one loadable field. Returned by [Describe] and
// consumed by [PrintUsage] / [Markdown]; users may also walk the slice
// programmatically (for instance to render their own help layout).
type FieldDoc struct {
	// Name is the resolved env-var name, with [WithPrefix] applied.
	Name string

	// GoType is the destination type in source notation, e.g.
	// "int", "time.Duration", "[]string", "*url.URL".
	GoType string

	// Description is the free-text help string from the envDesc tag.
	Description string

	// Default is the literal default value from the default= tag option.
	// Empty when no default is set.
	Default string

	// Required is true when the field carries the required tag option,
	// or implicitly required under [WithRequireAll].
	Required bool

	// NotEmpty is true when the field carries the notEmpty tag option.
	NotEmpty bool

	// Secret is true when the field carries the secret tag option.
	Secret bool

	// Immutable is true when the field carries the immutable tag option.
	Immutable bool

	// Separator is the slice/array separator from the separator= tag
	// option, or empty when defaulting to ",".
	Separator string
}

// Describe walks v's exported fields and returns a [FieldDoc] for each
// loadable leaf. Returns nil when v is not a struct (or pointer to one)
// or when the struct's tags fail to validate; use [Loader.PrintUsage]
// or [Loader.Markdown] for error-returning variants.
//
// opts apply to a fresh [Loader] (so callers can specify [WithPrefix]
// or [WithTagName] without constructing a Loader themselves).
func Describe(v any, opts ...Option) []FieldDoc {
	return New(opts...).Describe(v)
}

// PrintUsage writes a human-readable column-aligned table of v's
// loadable fields to w. Suitable for `myapp --env-help`-style output.
func PrintUsage(w io.Writer, v any, opts ...Option) error {
	return New(opts...).PrintUsage(w, v)
}

// Markdown writes a GitHub-Flavored Markdown table of v's loadable
// fields to w. Suitable for embedding in `docs/CONFIG.md` or `README.md`
// via `myapp --env-doc`.
func Markdown(w io.Writer, v any, opts ...Option) error {
	return New(opts...).Markdown(w, v)
}

// Describe is the [Loader]-method form of the package-level [Describe].
// The loader's [WithPrefix] is applied to each [FieldDoc.Name].
func (l *Loader) Describe(v any) []FieldDoc {
	rv, ok := describeRV(v)
	if !ok {
		return nil
	}
	info := l.lookupStructInfo(rv.Type())
	if info.Err != nil {
		return nil
	}
	return l.buildFieldDocs(rv.Type(), info)
}

// PrintUsage is the [Loader]-method form of [PrintUsage]. Returns an
// error when v is not a struct, when struct tags fail to validate, or
// when writing to w fails.
func (l *Loader) PrintUsage(w io.Writer, v any) error {
	docs, err := l.docsOrError(v)
	if err != nil {
		return err
	}
	return printUsageTable(w, docs)
}

// Markdown is the [Loader]-method form of [Markdown].
func (l *Loader) Markdown(w io.Writer, v any) error {
	docs, err := l.docsOrError(v)
	if err != nil {
		return err
	}
	return printMarkdownTable(w, docs)
}

// describeRV unwraps v to its struct value or reports failure.
func describeRV(v any) (reflect.Value, bool) {
	if v == nil {
		return reflect.Value{}, false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return rv, true
}

// docsOrError is the shared front-end for PrintUsage and Markdown:
// it returns the FieldDoc slice or a typed error explaining why none
// could be produced.
func (l *Loader) docsOrError(v any) ([]FieldDoc, error) {
	if v == nil {
		return nil, ErrNilPointer
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("got %T: %w", v, ErrNilPointer)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("got %T: %w", v, ErrNotStruct)
	}
	info := l.lookupStructInfo(rv.Type())
	if info.Err != nil {
		return nil, info.Err
	}
	return l.buildFieldDocs(rv.Type(), info), nil
}

// buildFieldDocs converts the loader's structInfo into the public
// FieldDoc slice. Names include the loader's prefix; GoType uses
// reflect.Type.String() ("int", "[]string", "time.Duration", etc.).
func (l *Loader) buildFieldDocs(t reflect.Type, info *structInfo) []FieldDoc {
	docs := make([]FieldDoc, 0, len(info.Fields))
	for _, fd := range info.Fields {
		field := t.FieldByIndex(fd.Index)
		docs = append(docs, FieldDoc{
			Name:        l.opts.prefix + fd.Key,
			GoType:      field.Type.String(),
			Description: fd.Description,
			Default:     fd.DefaultValue,
			Required:    fd.Required,
			NotEmpty:    fd.NotEmpty,
			Secret:      fd.Secret,
			Immutable:   fd.Immutable,
			Separator:   fd.Separator,
		})
	}
	return docs
}

// maxDefaultDisplayWidth caps the rendered width of the DEFAULT column
// in the text table. Defaults longer than this are truncated with an
// ellipsis to keep the table readable.
const maxDefaultDisplayWidth = 40

// printUsageTable renders docs as a column-aligned text table.
//
// Columns: NAME, TYPE, DEFAULT, REQUIRED, DESCRIPTION. Column widths
// are dynamic (max of header and longest field, capped at
// [maxDefaultDisplayWidth] for DEFAULT). The DESCRIPTION column is
// last, so no padding is applied to it.
//
// Annotations on the description: "(secret)" prefix for secret-tagged
// fields, "(notEmpty)" / "(immutable)" prefixes for those tag options.
func printUsageTable(w io.Writer, docs []FieldDoc) error {
	if len(docs) == 0 {
		return nil
	}

	const (
		headerName = "NAME"
		headerType = "TYPE"
		headerDef  = "DEFAULT"
		headerReq  = "REQUIRED"
		headerDesc = "DESCRIPTION"
		colPad     = 2
	)

	nameW := len(headerName)
	typeW := len(headerType)
	defW := len(headerDef)
	reqW := len(headerReq)

	for _, d := range docs {
		if l := len(d.Name); l > nameW {
			nameW = l
		}
		if l := len(d.GoType); l > typeW {
			typeW = l
		}
		dv := truncate(d.Default, maxDefaultDisplayWidth)
		if l := len(dv); l > defW {
			defW = l
		}
	}

	if _, err := fmt.Fprintf(w, "%-*s%-*s%-*s%-*s%s\n",
		nameW+colPad, headerName,
		typeW+colPad, headerType,
		defW+colPad, headerDef,
		reqW+colPad, headerReq,
		headerDesc,
	); err != nil {
		return fmt.Errorf("env: write header: %w", err)
	}

	for _, d := range docs {
		req := ""
		if d.Required {
			req = "yes"
		}
		desc := composeDescription(d)
		if _, err := fmt.Fprintf(w, "%-*s%-*s%-*s%-*s%s\n",
			nameW+colPad, d.Name,
			typeW+colPad, d.GoType,
			defW+colPad, truncate(d.Default, maxDefaultDisplayWidth),
			reqW+colPad, req,
			desc,
		); err != nil {
			return fmt.Errorf("env: write row: %w", err)
		}
	}
	return nil
}

// printMarkdownTable renders docs as a GitHub-Flavored Markdown table.
//
// Columns mirror PrintUsage. Cell values are wrapped in backticks where
// helpful (Name, GoType, Default) so the rendered output uses `code`
// formatting; pipe characters in cells are escaped as `\|`.
func printMarkdownTable(w io.Writer, docs []FieldDoc) error {
	if len(docs) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(w, "| Name | Type | Default | Required | Description |"); err != nil {
		return fmt.Errorf("env: write markdown header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "| ---- | ---- | ------- | -------- | ----------- |"); err != nil {
		return fmt.Errorf("env: write markdown separator: %w", err)
	}
	for _, d := range docs {
		def := ""
		if d.Default != "" {
			def = "`" + escapeMD(d.Default) + "`"
		}
		req := ""
		if d.Required {
			req = "yes"
		}
		desc := composeDescription(d)
		if _, err := fmt.Fprintf(w, "| `%s` | `%s` | %s | %s | %s |\n",
			escapeMD(d.Name),
			escapeMD(d.GoType),
			def,
			req,
			escapeMD(desc),
		); err != nil {
			return fmt.Errorf("env: write markdown row: %w", err)
		}
	}
	return nil
}

// composeDescription joins flag annotations with the user-supplied
// envDesc text. Annotations always precede the text and are space-
// separated. Order: secret, notEmpty, immutable, then desc.
func composeDescription(d FieldDoc) string {
	var prefixes []string
	if d.Secret {
		prefixes = append(prefixes, "(secret)")
	}
	if d.NotEmpty {
		prefixes = append(prefixes, "(notEmpty)")
	}
	if d.Immutable {
		prefixes = append(prefixes, "(immutable)")
	}
	if len(prefixes) == 0 {
		return d.Description
	}
	prefix := strings.Join(prefixes, " ")
	if d.Description == "" {
		return prefix
	}
	return prefix + " " + d.Description
}

// truncate clamps s to maxLen runes, replacing the tail with an
// ellipsis when truncation occurs. Operates on bytes for simplicity;
// non-ASCII identifiers in env-var names are exotic enough not to
// warrant rune-aware truncation here.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// escapeMD escapes the pipe character in markdown cell content. Other
// special characters are left as-is — most of them (asterisk, backtick,
// underscore) render harmlessly inside a code-span, which is how Name,
// GoType, and Default are formatted.
func escapeMD(s string) string {
	if !strings.ContainsRune(s, '|') {
		return s
	}
	return strings.ReplaceAll(s, "|", `\|`)
}
