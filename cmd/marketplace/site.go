package main

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"worm/internal/marketplace"
)

var packIndexTemplate = template.Must(template.New("packs").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>WORM parser packs</title>
<style>body{font:16px system-ui,sans-serif;max-width:960px;margin:2rem auto;padding:0 1rem;color:#20242a}li{margin:.8rem 0}small{color:#59636e}</style>
<h1>WORM parser packs</h1>
<p>{{len .}} packs. Select a pack to browse its published versions.</p>
<ul>{{range .}}<li><a href="{{.Name}}/index.html"><strong>{{.Name}}</strong></a> — {{.Description}}<br><small>{{.Vendor}} {{.Product}} · {{.Category}} · {{.Format}}</small></li>{{end}}</ul>
</html>
`))

var packVersionsTemplate = template.Must(template.New("pack").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}} — WORM parser pack</title>
<style>body{font:16px system-ui,sans-serif;max-width:960px;margin:2rem auto;padding:0 1rem;color:#20242a}li{margin:.8rem 0}small{color:#59636e}</style>
<p><a href="../index.html">← All parser packs</a></p>
<h1>{{.Name}}</h1>
<p>{{.Description}}</p>
<p><small>{{.Vendor}} {{.Product}} · {{.Category}} · {{.Format}}</small></p>
<h2>Published versions</h2>
<ul>{{range .Releases}}<li><a href="{{.Path}}">{{.Version}} — View pack YAML</a>{{if .Changelog}}<br><small>{{.Changelog}}</small>{{end}}</li>{{end}}</ul>
</html>
`))

func writePackIndexes(site string, catalog *marketplace.Catalog) error {
	root := filepath.Join(site, "packs")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	var listed []marketplace.Pack
	for _, pack := range catalog.Packs {
		if len(pack.Releases) > 0 {
			listed = append(listed, pack)
		}
	}
	if err := writeTemplate(filepath.Join(root, "index.html"), packIndexTemplate, listed); err != nil {
		return err
	}
	for _, pack := range listed {
		type releaseLink struct {
			Version, Path, Changelog string
		}
		type packPage struct {
			Name, Description, Vendor, Product, Category, Format string
			Releases                                             []releaseLink
		}
		page := packPage{Name: pack.Name, Description: pack.Description, Vendor: pack.Vendor, Product: pack.Product, Category: pack.Category, Format: pack.Format}
		prefix := "packs/" + pack.Name + "/"
		for _, release := range pack.Releases {
			page.Releases = append(page.Releases, releaseLink{
				Version: release.Version,
				Path:    strings.TrimPrefix(release.Artifact, prefix), Changelog: release.Changelog,
			})
		}
		dir := filepath.Join(root, pack.Name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if err := writeTemplate(filepath.Join(dir, "index.html"), packVersionsTemplate, page); err != nil {
			return err
		}
	}
	return nil
}

func writeTemplate(path string, tmpl *template.Template, data any) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := tmpl.Execute(file, data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
