// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gcimporter

import (
	"encoding/binary"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestIImportRejectsOversizedManifestCountBeforeAllocation(t *testing.T) {
	data := uvarintsForTest(
		iexportVersionCurrent,
		0,     // string data length
		0,     // decl data length
		1<<30, // manifest package count, but no bytes remain
	)

	_, err := iimportCommon(
		token.NewFileSet(),
		GetPackagesFromMap(map[string]*types.Package{}),
		data,
		false,
		"poisoned",
		false,
		nil,
	)
	if err == nil {
		t.Fatal("iimportCommon accepted oversized manifest count")
	}
	if !strings.Contains(err.Error(), "length prefix") {
		t.Fatalf("iimportCommon error = %v, want length-prefix rejection", err)
	}
}

func TestIImportShallowRejectsOversizedFileOffsetCountBeforeAllocation(t *testing.T) {
	data := uvarintsForTest(
		iexportVersionCurrent,
		0,     // string data length
		0,     // file data length
		1<<30, // file-offset count, but no bytes remain
	)

	_, err := iimportCommon(
		token.NewFileSet(),
		GetPackagesFromMap(map[string]*types.Package{}),
		data,
		false,
		"poisoned",
		true,
		nil,
	)
	if err == nil {
		t.Fatal("iimportCommon accepted oversized file-offset count")
	}
	if !strings.Contains(err.Error(), "length prefix") {
		t.Fatalf("iimportCommon error = %v, want length-prefix rejection", err)
	}
}

func uvarintsForTest(values ...uint64) []byte {
	var out []byte
	var buf [binary.MaxVarintLen64]byte
	for _, v := range values {
		n := binary.PutUvarint(buf[:], v)
		out = append(out, buf[:n]...)
	}
	return out
}
