package catalog

import (
	"embed"
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
)

// Bundles are built and browser-checked before release. Their verification is
// packaging evidence, never a claim that a visitor's prompt ran successfully.
//
//go:embed bundles/*.json
var bundles embed.FS

type Bundle struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Version      int               `json:"version"`
	Files        map[string]string `json:"files"`
	HTML         string            `json:"html"`
	SourceHashes map[string]string `json:"source_hashes"`
	HTMLHash     string            `json:"html_hash"`
	Verification struct {
		Kind   string       `json:"kind"`
		At     string       `json:"at"`
		Checks []core.Check `json:"checks"`
	} `json:"verification"`
}

func Get(id string) (Bundle, error) {
	if id != "board" && id != "reading" && id != "calculator" {
		return Bundle{}, core.ErrNotFound
	}
	data, err := bundles.ReadFile("bundles/" + id + ".json")
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if err = json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if b.ID != id || core.Hash([]byte(b.HTML)) != b.HTMLHash || len(b.Verification.Checks) == 0 {
		return b, errors.New("预置成果校验失败")
	}
	for name, content := range b.Files {
		if b.SourceHashes[name] != core.Hash([]byte(content)) {
			return b, errors.New("预置源码校验失败")
		}
	}
	for _, check := range b.Verification.Checks {
		if check.Status != "passed" {
			return b, errors.New("预置成果尚未通过检查")
		}
	}
	return b, nil
}

func List() ([]map[string]any, error) {
	out := []map[string]any{}
	for _, id := range []string{"board", "reading", "calculator"} {
		b, err := Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": b.ID, "title": b.Title, "description": b.Description, "version": b.Version, "source": "preset", "verified_at": b.Verification.At})
	}
	return out, nil
}

func (b Bundle) Project(owner, source string) (*core.Project, map[string][]byte, core.AppContract, error) {
	p := core.NewProject(owner, b.Title)
	p.Source = &core.ProjectSource{Kind: source, Template: b.ID, Version: b.Version}
	blobs := map[string][]byte{}
	for name, content := range b.Files {
		data := []byte(content)
		hash := core.Hash(data)
		blobs[hash] = data
		p.Files[name] = core.File{Hash: hash, Size: len(data), Kind: "code", ReadOnly: name == core.AppRuntimePath}
	}
	contract, err := core.ParseAppContract([]byte(b.Files[core.AppContractPath]))
	if err != nil {
		return nil, nil, contract, err
	}
	p.Reports = []core.Report{{ID: core.ID(), Kind: "packaged_browser", Digest: p.Files.Digest(), At: b.Verification.At, Status: "passed", Checks: b.Verification.Checks, Details: json.RawMessage(core.JSON(map[string]any{"preview_html": b.HTML, "source": "preset", "template": b.ID, "template_version": b.Version, "html_hash": b.HTMLHash, "notice": "发布前模板检查；未调用模型执行当前需求"}))}}
	p.Head = core.ID()
	p.Versions = []core.Version{{ID: p.Head, Number: 1, At: core.Now(), Summary: "预置示例副本 · " + b.Title, Manifest: core.Clone(p.Files), Reports: p.Reports}}
	p.Message("assistant", "这是可交互、可保存的预置示例副本。可直接使用，也可提交需求继续修改。", "preset")
	return p, blobs, contract, nil
}
