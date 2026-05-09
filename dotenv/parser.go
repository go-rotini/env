package dotenv

// parse runs the scanner and the optional expansion pass.
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
