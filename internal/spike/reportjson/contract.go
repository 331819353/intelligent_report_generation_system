package reportjson

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Document struct {
	SchemaVersion string  `json:"schemaVersion"`
	ID            string  `json:"id"`
	Canvas        Canvas  `json:"canvas"`
	Blocks        []Block `json:"blocks"`
}
type Canvas struct {
	Width, ViewportHeight, Columns int
	RowsPerViewport                int
	ActualHeight                   int
}
type Block struct {
	ID         string `json:"id"`
	X, Y, W, H int
	Components []json.RawMessage `json:"components"`
}

// Validate 校验报表 JSON 的版本、组件标识与组件类型约束。
func Validate(d Document) error {
	if d.SchemaVersion != "1.0" || d.ID == "" {
		return errors.New("invalid report identity")
	}
	if d.Canvas.Width != 1920 || d.Canvas.ViewportHeight != 1080 || d.Canvas.Columns != 12 || d.Canvas.RowsPerViewport != 10 || d.Canvas.ActualHeight < 1080 {
		return errors.New("invalid canvas")
	}
	for _, b := range d.Blocks {
		if b.ID == "" || b.X < 0 || b.Y < 0 || b.W < 1 || b.H < 1 || b.X+b.W > 12 {
			return errors.New("invalid block")
		}
	}
	return nil
}

// ContractHash 生成稳定哈希，用于识别报表契约是否发生变化。
func ContractHash(d Document) (string, error) {
	if err := Validate(d); err != nil {
		return "", err
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
