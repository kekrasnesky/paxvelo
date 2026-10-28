package main

import (
	"io/fs"
	"regexp"
	"testing"
)

// Every {{icon "name"}} in the templates names an icon that exists, so no
// page fails to render over a typo.
func TestTemplateIconsExist(t *testing.T) {
	use := regexp.MustCompile(`\{\{icon "([^"]+)"\}\}`)
	found := 0
	fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(webFS, path)
		for _, m := range use.FindAllStringSubmatch(string(b), -1) {
			found++
			if _, err := icon(m[1]); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		return nil
	})
	if found == 0 {
		t.Error("no icons found in the templates")
	}
}
