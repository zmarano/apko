// Copyright 2022, 2023 Chainguard, Inc.
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

package build

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	gzip "github.com/klauspost/pgzip"
	"go.opentelemetry.io/otel"

	apkfs "github.com/chainguard-dev/go-apk/pkg/fs"
	"github.com/chainguard-dev/go-apk/pkg/tarball"
	"github.com/sigstore/cosign/v2/pkg/oci"
	"gitlab.alpinelinux.org/alpine/go/repository"

	chainguardAPK "chainguard.dev/apko/pkg/apk"
	"chainguard.dev/apko/pkg/options"
)

// BuildTarball takes the fully populated working directory and saves it to
// an OCI image layer tar.gz file.
func (bc *Context) BuildTarball(ctx context.Context) (string, hash.Hash, hash.Hash, int64, error) {
	ctx, span := otel.Tracer("apko").Start(ctx, "BuildTarball")
	defer span.End()

	var outfile *os.File
	var err error

	if bc.o.TarballPath != "" {
		outfile, err = os.Create(bc.o.TarballPath)
	} else {
		outfile, err = os.Create(filepath.Join(bc.o.TempDir(), bc.o.TarballFileName()))
	}
	if err != nil {
		return "", nil, nil, 0, fmt.Errorf("opening the build context tarball path failed: %w", err)
	}
	bc.o.TarballPath = outfile.Name()
	defer outfile.Close()

	// we use a general override of 0,0 for all files, but the specific overrides, that come from the installed package DB, come later
	tw, err := tarball.NewContext(
		tarball.WithSourceDateEpoch(bc.o.SourceDateEpoch),
	)
	if err != nil {
		return "", nil, nil, 0, fmt.Errorf("failed to construct tarball build context: %w", err)
	}

	digest := sha256.New()

	buf := bufio.NewWriterSize(outfile, 1<<22)
	gzw := gzip.NewWriter(io.MultiWriter(digest, buf))

	diffid := sha256.New()

	if err := tw.WriteTar(ctx, io.MultiWriter(diffid, gzw), bc.fs); err != nil {
		return "", nil, nil, 0, fmt.Errorf("failed to generate tarball for image: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return "", nil, nil, 0, fmt.Errorf("closing gzip writer: %w", err)
	}

	if err := buf.Flush(); err != nil {
		return "", nil, nil, 0, fmt.Errorf("flushing %s: %w", outfile.Name(), err)
	}

	stat, err := outfile.Stat()
	if err != nil {
		return "", nil, nil, 0, fmt.Errorf("stat(%q): %w", outfile.Name(), err)
	}

	bc.Logger().Infof("built image layer tarball as %s", outfile.Name())
	return outfile.Name(), diffid, digest, stat.Size(), nil
}

func additionalTags(fsys apkfs.FullFS, o *options.Options) error {
	at, err := chainguardAPK.AdditionalTags(fsys, *o)
	if err != nil {
		return err
	}
	if at == nil {
		return nil
	}
	o.Tags = append(o.Tags, at...)
	return nil
}

func (bc *Context) buildImage(ctx context.Context) error {
	ctx, span := otel.Tracer("apko").Start(ctx, "buildImage")
	defer span.End()

	if err := bc.apk.FixateWorld(ctx, &bc.o.SourceDateEpoch); err != nil {
		return fmt.Errorf("installing apk packages: %w", err)
	}

	if err := additionalTags(bc.fs, &bc.o); err != nil {
		return fmt.Errorf("adding additional tags: %w", err)
	}

	if err := mutateAccounts(bc.fs, &bc.o, &bc.ic); err != nil {
		return fmt.Errorf("failed to mutate accounts: %w", err)
	}

	if err := mutatePaths(bc.fs, &bc.o, &bc.ic); err != nil {
		return fmt.Errorf("failed to mutate paths: %w", err)
	}

	if err := GenerateOSRelease(bc.fs, &bc.o, &bc.ic); err != nil {
		if errors.Is(err, ErrOSReleaseAlreadyPresent) {
			bc.Logger().Warnf("did not generate /etc/os-release: %v", err)
		} else {
			return fmt.Errorf("failed to generate /etc/os-release: %w", err)
		}
	}

	if err := bc.s6.WriteSupervisionTree(bc.ic.Entrypoint.Services); err != nil {
		return fmt.Errorf("failed to write supervision tree: %w", err)
	}

	// add busybox symlinks
	installed, err := bc.apk.GetInstalled()
	if err != nil {
		return fmt.Errorf("getting installed packages: %w", err)
	}

	if err := installBusyboxLinks(bc.fs, installed); err != nil {
		return err
	}

	// add ldconfig links
	if err := installLdconfigLinks(bc.fs); err != nil {
		return err
	}

	// add necessary character devices
	if err := installCharDevices(bc.fs); err != nil {
		return err
	}

	bc.Logger().Infof("finished building filesystem in %s", bc.o.WorkDir)

	return nil
}

// WriteIndex saves the index file from the given image configuration.
func (bc *Context) WriteIndex(idx oci.SignedImageIndex) (string, int64, error) {
	outfile := filepath.Join(bc.o.TempDir(), "index.json")

	b, err := idx.RawManifest()
	if err != nil {
		return "", 0, fmt.Errorf("getting raw manifest: %w", err)
	}
	if err := os.WriteFile(outfile, b, 0644); err != nil { //nolint:gosec // this file is fine to be readable
		return "", 0, fmt.Errorf("writing index file: %w", err)
	}

	stat, err := os.Stat(outfile)
	if err != nil {
		return "", 0, fmt.Errorf("stat(%q): %w", outfile, err)
	}

	bc.Logger().Infof("built index file as %s", outfile)
	return outfile, stat.Size(), nil
}

func (bc *Context) BuildPackageList(ctx context.Context) (toInstall []*repository.RepositoryPackage, conflicts []string, err error) {
	if toInstall, conflicts, err = bc.apk.ResolveWorld(ctx); err != nil {
		return toInstall, conflicts, fmt.Errorf("resolving apk packages: %w", err)
	}
	bc.Logger().Infof("finished gathering apk info in %s", bc.o.WorkDir)

	return toInstall, conflicts, err
}
