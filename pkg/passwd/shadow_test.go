// Copyright 2025 Chainguard, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package passwd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	apkfs "chainguard.dev/apko/pkg/apk/fs"
)

func TestShadowParser(t *testing.T) {
	fsys := apkfs.DirFS("testdata")
	sf, err := ReadOrCreateShadowFile(fsys, "shadow")
	require.NoError(t, err)

	for _, se := range sf.Entries {
		if se.UserName == "root" {
			require.Equal(t, "*", se.EncPassword, "password for root is not *")
		}
	}
}

func TestShadowWriter(t *testing.T) {
	fsys := apkfs.DirFS("testdata")
	sf, err := ReadOrCreateShadowFile(fsys, "shadow")
	require.NoError(t, err)

	w := &bytes.Buffer{}
	require.NoError(t, sf.Write(w))

	r := bytes.NewReader(w.Bytes())
	sf2 := &ShadowFile{}
	require.NoError(t, sf2.Load(r))

	w2 := &bytes.Buffer{}
	require.NoError(t, sf2.Write(w2))

	require.Equal(t, w.Bytes(), w2.Bytes())
}
