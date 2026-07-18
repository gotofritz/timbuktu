package prompts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// TemplateData is passed to both system.tmpl and user.tmpl during rendering.
type TemplateData struct {
	Question  string
	Chunks    []retrieval.RetrievedChunk
	Variables map[string]string
}

// Template holds a loaded prompt template ready to render.
type Template struct {
	manifest Manifest
	system   *template.Template
	user     *template.Template
}

// Manifest returns the template's parsed manifest.
func (t *Template) Manifest() Manifest { return t.manifest }

// Render executes system and user templates with the given data.
func (t *Template) Render(data TemplateData) (system, user string, err error) {
	var sb, ub bytes.Buffer
	if err := t.system.Execute(&sb, data); err != nil {
		return "", "", fmt.Errorf("render system template: %w", err)
	}
	if err := t.user.Execute(&ub, data); err != nil {
		return "", "", fmt.Errorf("render user template: %w", err)
	}
	return sb.String(), ub.String(), nil
}

// TemplateDir is the root directory that holds named template subdirectories.
type TemplateDir struct {
	Root string
}

// NewTemplateDir returns a TemplateDir rooted at dir.
func NewTemplateDir(dir string) *TemplateDir {
	return &TemplateDir{Root: dir}
}

// Load reads the named template from Root/<name>/ and parses all files.
func (td *TemplateDir) Load(name string) (*Template, error) {
	dir := filepath.Join(td.Root, name)

	manifest, err := loadManifest(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, err
	}

	sysTmpl, err := loadTextTemplate("system", filepath.Join(dir, "system.tmpl"))
	if err != nil {
		return nil, err
	}
	usrTmpl, err := loadTextTemplate("user", filepath.Join(dir, "user.tmpl"))
	if err != nil {
		return nil, err
	}

	return &Template{manifest: manifest, system: sysTmpl, user: usrTmpl}, nil
}

// List returns the manifests for every valid template in Root.
// Templates that are missing manifest.yaml are silently skipped.
func (td *TemplateDir) List() ([]Manifest, error) {
	entries, err := os.ReadDir(td.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list templates in %s: %w", td.Root, err)
	}

	var manifests []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mp := filepath.Join(td.Root, e.Name(), "manifest.yaml")
		m, err := loadManifest(mp)
		if err != nil {
			continue // skip dirs without a valid manifest
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

func loadTextTemplate(name, path string) (*template.Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read template file %s: %w", path, err)
	}
	t, err := template.New(name).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", path, err)
	}
	return t, nil
}
