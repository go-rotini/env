package dotenv

// parse is the entry point for converting source bytes into a [*File].
//
// The scanner produces logical entries directly, so this layer is thin:
// it just delegates and applies the optional post-parse expansion pass.
func parse(src []byte, opts parseOptions) (*File, error) {
	f, err := scanFile(src, opts)
	if err != nil {
		return nil, err
	}
	if opts.expand {
		if err := f.expandAll(opts); err != nil {
			return nil, err
		}
	}
	return f, nil
}
