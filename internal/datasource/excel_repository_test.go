package datasource

import "testing"

func TestFileReuploadUsesCanonicalSourceConfigurationHash(t *testing.T) {
	source := Source{
		Type: TypeExcel,
		Config: map[string]any{
			"selectedSheets": []any{"Sales"},
			"headerRow":      1,
		},
		FileAssetID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	const versionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	reuploadHash, err := fileSourceVersionConfigurationHash(source, versionID)
	if err != nil {
		t.Fatal(err)
	}
	createOrUpdate := source
	createOrUpdate.FileVersionID = versionID
	canonicalHash, err := sourceConfigurationHash(createOrUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if reuploadHash != canonicalHash {
		t.Fatalf("reupload hash %s differs from create/update hash %s", reuploadHash, canonicalHash)
	}
	const expected = "d1c4be3dede84d13ff1d6da021d6d3613681adebec507b4fcffdf65e50715eee"
	if reuploadHash != expected {
		t.Fatalf("canonical configuration hash=%s, want %s", reuploadHash, expected)
	}

	nextHash, err := fileSourceVersionConfigurationHash(
		source,
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	)
	if err != nil {
		t.Fatal(err)
	}
	if nextHash == reuploadHash {
		t.Fatal("a new immutable file version reused the prior configuration hash")
	}
}
