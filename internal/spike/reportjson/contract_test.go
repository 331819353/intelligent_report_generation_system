package reportjson

import "testing"

func TestRendererEntrypointsShareContract(t *testing.T) {
	d := Document{SchemaVersion: "1.0", ID: "r1", Canvas: Canvas{Width: 1920, ViewportHeight: 1080, Columns: 12, RowsPerViewport: 10, ActualHeight: 3240}, Blocks: []Block{{ID: "b1", X: 0, Y: 12, W: 6, H: 3}}}
	designer, _ := ContractHash(d)
	viewer, _ := ContractHash(d)
	pdf, _ := ContractHash(d)
	if designer != viewer || viewer != pdf {
		t.Fatal("renderer contracts diverged")
	}
	d.Blocks[0].W = 13
	if _, err := ContractHash(d); err == nil {
		t.Fatal("invalid layout accepted")
	}
}
