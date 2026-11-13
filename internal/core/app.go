package core

import (
	"encoding/json"
	"errors"
)

const AppContractPath = "atoms.app.json"
const AppRuntimePath = "atoms-runtime.js"
const MaxAppStateBytes = 256 * 1024

// AppContract declares the format of business data, independently of code versions.
type AppContract struct {
	API     int             `json:"api_version"`
	Schema  int             `json:"schema_version"`
	Initial json.RawMessage `json:"initial_state"`
}

func ParseAppContract(data []byte) (AppContract, error) {
	var c AppContract
	if json.Unmarshal(data, &c) != nil || c.API != 1 || c.Schema < 1 || c.Schema > 1000000 || ValidateAppValue(c.Initial) != nil {
		return c, errors.New("应用数据契约无效：需要 api_version=1、正整数 schema_version 和 initial_state 对象")
	}
	return c, nil
}

func ValidateAppValue(data []byte) error {
	var value map[string]json.RawMessage
	if len(data) > MaxAppStateBytes || json.Unmarshal(data, &value) != nil || value == nil {
		return errors.New("应用数据必须是 JSON 对象，且不超过 256 KiB")
	}
	return nil
}

func (p *Project) PreviewFiles(version string) (Manifest, []Report, error) {
	if version == "" {
		return p.Files, p.Reports, nil
	}
	for _, v := range p.Versions {
		if v.ID == version {
			return v.Manifest, v.Reports, nil
		}
	}
	return nil, nil, ErrNotFound
}

type AppState struct {
	Schema   int             `json:"schema_version"`
	Revision int64           `json:"revision"`
	Value    json.RawMessage `json:"value"`
	Updated  string          `json:"updated"`
}
