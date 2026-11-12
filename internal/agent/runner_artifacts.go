package agent

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"unicode/utf8"
)

type runnerArtifact struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Data   string `json:"data_base64"`
	Hash   string `json:"sha256"`
	Size   int    `json:"size"`
}

func validateOutputDeclarations(outputs []core.OutputDeclaration, inputs core.Manifest) error {
	if len(outputs) > 20 {
		return errors.New("最多声明 20 个输出文件")
	}
	seen := map[string]bool{}
	for _, output := range outputs {
		if _, err := core.FilePath(output.Path); err != nil {
			return err
		}
		if seen[output.Path] || (output.Format != "text" && output.Format != "binary") {
			return errors.New("输出路径重复或格式无效")
		}
		seen[output.Path] = true
		file := inputs[output.Path]
		if file.ReadOnly || file.Hash != output.OldHash {
			return fmt.Errorf("输出 %s 为只读或旧版本不匹配", output.Path)
		}
	}
	return nil
}

func collectRunnerArtifacts(declared []core.OutputDeclaration, actual []runnerArtifact, inputs core.Manifest) (map[string][]byte, core.Manifest, error) {
	if err := validateOutputDeclarations(declared, inputs); err != nil {
		return nil, nil, err
	}
	if len(declared) != len(actual) {
		return nil, nil, errors.New("回收输出数量与声明不一致")
	}
	wanted := map[string]core.OutputDeclaration{}
	for _, d := range declared {
		wanted[d.Path] = d
	}
	blobs := map[string][]byte{}
	outputs := core.Manifest{}
	total := 0
	for _, artifact := range actual {
		declaration, ok := wanted[artifact.Path]
		if !ok || outputs[artifact.Path].Hash != "" || artifact.Format != declaration.Format {
			return nil, nil, errors.New("回收了未声明、重复或格式不匹配的文件")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(artifact.Data)
		if err != nil || artifact.Data != base64.StdEncoding.EncodeToString(data) {
			return nil, nil, errors.New("产物二进制传输编码无效")
		}
		if len(data) != artifact.Size || len(data) > core.MaxFileBytes || core.Hash(data) != artifact.Hash {
			return nil, nil, errors.New("产物大小或哈希校验失败")
		}
		total += len(data)
		if total > core.MaxProjectBytes {
			return nil, nil, errors.New("回收产物总大小超限")
		}
		if artifact.Format == "text" && !utf8.Valid(data) {
			return nil, nil, errors.New("文本产物不是有效 UTF-8")
		}
		fileKind := kind(artifact.Path)
		if artifact.Format == "binary" {
			fileKind = "artifact"
		}
		outputs[artifact.Path] = core.File{Hash: artifact.Hash, Size: len(data), Kind: fileKind, Binary: artifact.Format == "binary"}
		blobs[artifact.Hash] = data
	}
	return blobs, outputs, nil
}

func (e *Engine) runnerFiles(owner string, inputs core.Manifest) (map[string]string, map[string]string, error) {
	text, binary := map[string]string{}, map[string]string{}
	for name, file := range inputs {
		data, err := e.Store.Blob(owner, file.Hash)
		if err != nil {
			return nil, nil, err
		}
		if file.Binary {
			binary[name] = base64.StdEncoding.EncodeToString(data)
		} else {
			text[name] = string(data)
		}
	}
	return text, binary, nil
}
