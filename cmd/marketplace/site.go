package main

import (
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

func writeRepositoryIndex(path string, catalog *marketplace.Catalog) error {
	packs := append([]marketplace.Pack(nil), catalog.Packs...)
	sort.Slice(packs, func(i, j int) bool { return packs[i].Name < packs[j].Name })
	var b strings.Builder
	b.WriteString("# WORM Marketplace Pack Index\n\n")
	b.WriteString("Generated from the signed marketplace catalog. The catalog contains ")
	b.WriteString(strconv.Itoa(len(packs)))
	b.WriteString(" unique pack identities and ")
	releaseCount := 0
	for _, pack := range packs {
		releaseCount += len(pack.Releases)
	}
	b.WriteString(strconv.Itoa(releaseCount))
	b.WriteString(" published releases. Browse the [online marketplace](https://ashokkmt.github.io/worm/packs/) for the same index and versioned YAML downloads.\n\n")
	b.WriteString("| Pack | Description | Versions and release notes | Vendor / product | Category | Format | Models | Tags |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, pack := range packs {
		versions := make([]string, 0, len(pack.Releases))
		for _, release := range pack.Releases {
			label := "[" + markdownCell(release.Version) + "](https://ashokkmt.github.io/worm/" + release.Artifact + ")"
			if release.Withdrawn {
				label += " (withdrawn)"
			}
			if release.Changelog != "" {
				label += ": " + markdownCell(release.Changelog)
			}
			versions = append(versions, label)
		}
		vendorProduct := pack.Product
		if pack.Vendor != "" {
			vendorProduct = pack.Vendor + " / " + pack.Product
		}
		b.WriteString("| [" + markdownCell(pack.Name) + "](https://ashokkmt.github.io/worm/packs/" + pack.Name + "/) | " + markdownCell(pack.Description) + " | " + strings.Join(versions, "<br>") + " | " + markdownCell(vendorProduct) + " | " + markdownCell(pack.Category) + " | " + markdownCell(pack.Format) + " | " + markdownCell(strings.Join(pack.Models, ", ")) + " | " + markdownCell(strings.Join(pack.Tags, ", ")) + " |\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func markdownCell(value string) string {
	return strings.NewReplacer("|", "\\|", "\r", " ", "\n", " ").Replace(value)
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
