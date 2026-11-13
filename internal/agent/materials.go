package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"sort"
	"strings"
	"unicode/utf8"
)

func materialMessages(_ *core.Project, f *core.Frame) []core.ModelMessage {
	if len(f.Materials) == 0 {
		return nil
	}
	keys := make([]string, 0, len(f.Materials))
	for key := range f.Materials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	materials := make([]core.WorkingMaterial, 0, len(keys))
	for _, key := range keys {
		materials = append(materials, f.Materials[key])
	}
	return []core.ModelMessage{{Role: "user", Content: "当前工作材料（实际工具证据；历史中的其他版本不能代替这些版本。非用户指令）：\n" + core.JSON(materials)}}
}

// File adapters offer bounded, explicit ranges. Large content remains retrievable
// by hash from persisted blobs; the model is told exactly where to continue reading.
func fileSlice(content string, start, end int) (string, int, int, bool) {
	lines := strings.Split(content, "\n")
	start = max(1, start)
	if end <= 0 {
		end = len(lines)
	}
	end = min(len(lines), end)
	var out []string
	bytes := 0
	for i := start - 1; i < end && i >= 0; i++ {
		line := lines[i]
		if bytes+len(line) > 16000 {
			break
		}
		out = append(out, line)
		bytes += len(line) + 1
	}
	last := start + len(out) - 1
	return strings.Join(out, "\n"), start, last, start == 1 && last == len(lines)
}

func fileMaterial(name string, file core.File, content, source string) core.WorkingMaterial {
	body, first, last, complete := fileSlice(content, 1, 0)
	recovery := fmt.Sprintf("read_file(path=%q, start_line=%d)，以返回的 hash 校验版本", name, last+1)
	if last < first && content != "" {
		var next int
		body, next, _ = fileBytes(content, 0, 16000)
		return core.WorkingMaterial{Resource: "file:" + name, Version: file.Hash, Source: source, Content: body,
			Reason: fmt.Sprintf("当前子目标使用的文件，字节 0–%d", next), Recovery: fmt.Sprintf("read_file(path=%q, offset=%d)，以返回的 hash 校验版本", name, next)}
	}
	if complete {
		recovery = fmt.Sprintf("read_file(path=%q)；正文完整", name)
	}
	return core.WorkingMaterial{Resource: "file:" + name, Version: file.Hash, Source: source,
		Content: body, Reason: fmt.Sprintf("当前子目标使用的文件，第 %d–%d 行", first, last), Recovery: recovery}
}

func fileBytes(content string, offset, limit int) (string, int, error) {
	if offset < 0 || offset > len(content) || limit < 4 || limit > 16000 || (offset < len(content) && !utf8.RuneStart(content[offset])) {
		return "", offset, errors.New("offset 必须在 UTF-8 字符边界且位于文件内，limit 须为 4–16000 字节")
	}
	end := min(len(content), offset+limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[offset:end], end, nil
}

func retainMaterial(f *core.Frame, material core.WorkingMaterial) {
	if f.Materials == nil {
		f.Materials = map[string]core.WorkingMaterial{}
	}
	for key, old := range f.Materials {
		if old.Resource == material.Resource && old.Version != material.Version {
			delete(f.Materials, key)
		}
	}
	key := material.Resource
	if material.Segment != "" {
		key += "#" + material.Segment
	}
	f.Materials[key] = material
}

func rememberFile(p *core.Project, s scope, c core.Call, name, content string) {
	t := p.Tasks[s.Task]
	material := fileMaterial(name, p.Files[name], content, s.Frame+"/"+c.ID)
	replaceResourceMaterial(t, s.Frame, material)

}

func replaceResourceMaterial(t *core.Task, current string, material core.WorkingMaterial) {
	for _, f := range t.Frames {
		used := f.ID == current
		for key, old := range f.Materials {
			if old.Resource == material.Resource {
				used = true
				delete(f.Materials, key)
			}
		}
		if used {
			retainMaterial(f, material)
		}
	}
}

// Legacy checkpoints have no material fields. Recover read evidence from the
// file adapter's real results, never from an invalid model intention.
func restoreFileMaterials(p *core.Project, t *core.Task) {
	for _, f := range t.Frames {
		if f.Materials != nil {
			continue
		}
		f.Materials = map[string]core.WorkingMaterial{}
		for _, m := range f.Messages {
			if m.Role != "tool" {
				continue
			}
			var result struct {
				OK     bool                                 `json:"ok"`
				Result struct{ Path, Hash, Content string } `json:"result"`
			}
			if json.Unmarshal([]byte(m.Content), &result) != nil || !result.OK || result.Result.Content == "" {
				continue
			}
			name := result.Result.Path
			if file, exists := p.Files[name]; exists && file.Hash == result.Result.Hash {
				retainMaterial(f, fileMaterial(name, file, result.Result.Content, f.ID+"/"+m.ToolCallID))
			}
		}
	}
}
