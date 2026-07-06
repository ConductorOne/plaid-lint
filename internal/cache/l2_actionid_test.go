package cache

import (
	"testing"
)

func TestComputeL2ActionIDDeterministic(t *testing.T) {
	e := sampleL2()
	a := ComputeL2ActionID(e)
	b := ComputeL2ActionID(e)
	if a != b {
		t.Errorf("same entry → different action IDs: %x vs %x", a, b)
	}
}

func TestComputeL2ActionIDInputDigestSensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.InputDigest[0] ^= 0xff
	b := ComputeL2ActionID(e)
	if a == b {
		t.Errorf("InputDigest change did not change action ID")
	}
}

func TestComputeL2ActionIDDepDigestSensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.DepTypeDigest[7] ^= 0xff
	b := ComputeL2ActionID(e)
	if a == b {
		t.Errorf("DepTypeDigest change did not change action ID")
	}
}

func TestComputeL2ActionIDPackageIDSensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.PackageID = "github.com/example/bar"
	b := ComputeL2ActionID(e)
	if a == b {
		t.Errorf("PackageID change did not change action ID")
	}
}

func TestComputeL2ActionIDGoVersionSensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.GoVersion = "go1.27"
	b := ComputeL2ActionID(e)
	if a == b {
		t.Errorf("GoVersion change did not change action ID")
	}
}

func TestComputeL2ActionIDBuildEnvSensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.BuildEnv = "darwin/amd64/cgo0"
	b := ComputeL2ActionID(e)
	if a == b {
		t.Errorf("BuildEnv change did not change action ID")
	}
}

func TestComputeL2ActionIDExportDataInsensitive(t *testing.T) {
	// ExportData is output, not input — it must not feed the action ID.
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.ExportData = []byte("entirely-different-blob")
	b := ComputeL2ActionID(e)
	if a != b {
		t.Errorf("ExportData change leaked into action ID")
	}
}

func TestComputeL2ActionIDFactsBlobInsensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.FactsBlob = []byte("entirely-different-facts")
	b := ComputeL2ActionID(e)
	if a != b {
		t.Errorf("FactsBlob change leaked into action ID")
	}
}

func TestComputeL2ActionIDFileSetSnapshotInsensitive(t *testing.T) {
	a := ComputeL2ActionID(sampleL2())
	e := sampleL2()
	e.FileSetSnapshot = []byte("different-fileset")
	b := ComputeL2ActionID(e)
	if a != b {
		t.Errorf("FileSetSnapshot change leaked into action ID")
	}
}
