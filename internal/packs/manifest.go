package packs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type PackInstall struct {
	Name string `json:"name"`
	Filename string `json:"filename"`
	Version string `json:"version"`
	Digest string `json:"digest"`
	Origin string `json:"origin"`
	Pinned bool `json:"pinned"`
}

type PackRevision struct {
	Filename string `json:"filename"`
	Existed bool `json:"existed"`
	Content []byte `json:"content,omitempty"`
	Previous *PackInstall `json:"previous,omitempty"`
}

type PackManifest struct {
	Version int `json:"version"`
	Entries map[string]PackInstall `json:"entries"`
	History map[string][]PackRevision `json:"history"`
}

func manifestPath(dir string) string { return filepath.Join(dir,".marketplace","manifest.json") }

func EmptyManifest() PackManifest { return PackManifest{Version:1,Entries:map[string]PackInstall{},History:map[string][]PackRevision{}} }

func ReadManifest(dir string) (PackManifest,error) {
	b,err:=os.ReadFile(manifestPath(dir))
	if os.IsNotExist(err) { return EmptyManifest(),nil }
	if err!=nil { return PackManifest{},err }
	var m PackManifest
	if err=json.Unmarshal(b,&m);err!=nil{return PackManifest{},err}
	if m.Version!=1{return PackManifest{},errors.New("unsupported parser pack manifest version")}
	if m.Entries==nil{m.Entries=map[string]PackInstall{}}
	if m.History==nil{m.History=map[string][]PackRevision{}}
	return m,nil
}

func encodeManifest(m PackManifest) ([]byte,error) { return json.MarshalIndent(m,"","  ") }

func WriteManifest(dir string,m PackManifest) error {
	b,err:=encodeManifest(m);if err!=nil{return err}
	return atomicWrite(manifestPath(dir),b,0600)
}
