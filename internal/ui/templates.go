package ui

import (
	"html/template"
	"io/fs"
	"path"
)

// Templates parses every .html file under templates/ with funcs available,
// plus this package's builtins (icon, tileClass, tileLetter, dict, list), and
// returns a function that yields
// the result. A release build parses the
// embedded files once, here; a dev build re-reads them from disk on every
// call, so edits show on refresh.
func Templates(funcs template.FuncMap) (func() (*template.Template, error), error) {
	parse := func() (*template.Template, error) { return parseTemplates(files, funcs) }
	t, err := parse()
	if err != nil {
		return nil, err
	}
	if dev {
		return parse, nil
	}
	return func() (*template.Template, error) { return t, nil }, nil
}

func parseTemplates(fsys fs.FS, funcs template.FuncMap) (*template.Template, error) {
	var names []string
	err := fs.WalkDir(fsys, "templates", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path.Ext(name) == ".html" {
			names = append(names, name)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return template.New("").Funcs(builtins).Funcs(funcs).ParseFS(fsys, names...)
}
