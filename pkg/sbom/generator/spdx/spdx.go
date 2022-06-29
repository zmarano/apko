// Copyright 2022 Chainguard, Inc.
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

package spdx

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.alpinelinux.org/alpine/go/pkg/repository"
	"sigs.k8s.io/release-utils/version"

	purl "github.com/package-url/packageurl-go"

	"chainguard.dev/apko/pkg/sbom/options"
)

// https://spdx.github.io/spdx-spec/3-package-information/#32-package-spdx-identifier
var validIDCharsRe = regexp.MustCompile(`[^a-zA-Z0-9-.]+`)

const NOASSERTION = "NOASSERTION"

type SPDX struct{}

func New() SPDX {
	return SPDX{}
}

func (sx *SPDX) Key() string {
	return "spdx"
}

func (sx *SPDX) Ext() string {
	return "spdx.json"
}

func stringToIdentifier(in string) (out string) {
	return validIDCharsRe.ReplaceAllStringFunc(in, func(s string) string {
		r := ""
		for i := 0; i < len(s); i++ {
			uc, _ := utf8.DecodeRuneInString(string(s[i]))
			r = fmt.Sprintf("%sC%d", r, uc)
		}
		return r
	})
}

// Generate writes a cyclondx sbom in path
func (sx *SPDX) Generate(opts *options.Options, path string) error {
	// The default document name makes no attempt to avoid
	// clashes. Ensuring a unique name requires a digest
	documentName := "sbom"
	if opts.ImageInfo.LayerDigest != "" {
		documentName += "-" + opts.ImageInfo.LayerDigest
	}
	doc := &Document{
		ID:      "SPDXRef-DOCUMENT",
		Name:    documentName,
		Version: "SPDX-2.2",
		CreationInfo: CreationInfo{
			Created: opts.ImageInfo.SourceDateEpoch.Format(time.RFC3339),
			Creators: []string{
				fmt.Sprintf("Tool: apko (%s)", version.GetVersionInfo().GitVersion),
				"Organization: Chainguard, Inc",
			},
			LicenseListVersion: "3.16",
		},
		DataLicense:   "CC0-1.0",
		Namespace:     "https://spdx.org/spdxdocs/apko/",
		Packages:      []Package{},
		Relationships: []Relationship{},
	}
	var imagePackage *Package
	layerPackage, err := sx.layerPackage(opts)
	if err != nil {
		return fmt.Errorf("generating layer spdx package: %w", err)
	}

	doc.DocumentDescribes = []string{layerPackage.ID}

	if opts.ImageInfo.ImageDigest != "" {
		imagePackage = sx.imagePackage(opts)
		doc.DocumentDescribes = []string{imagePackage.ID}
		doc.Packages = append(doc.Packages, *imagePackage)
		// Add to the relationships list
		doc.Relationships = append(doc.Relationships, Relationship{
			Element: imagePackage.ID,
			Type:    "CONTAINS",
			Related: layerPackage.ID,
		})
	}

	doc.Packages = append(doc.Packages, *layerPackage)

	for _, pkg := range opts.Packages {
		// add the package
		p, err := sx.apkPackage(opts, pkg)
		if err != nil {
			return fmt.Errorf("generating apk package: %w", err)
		}
		// Add the layer to the ID to avoid clashes
		p.ID = stringToIdentifier(fmt.Sprintf(
			"SPDXRef-Package-%s-%s-%s", layerPackage.ID, pkg.Name, pkg.Version,
		))

		doc.Packages = append(doc.Packages, p)

		// Add to the relationships list
		doc.Relationships = append(doc.Relationships, Relationship{
			Element: layerPackage.ID,
			Type:    "CONTAINS",
			Related: p.ID,
		})
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("opening SBOM path %s for writing: %w", path, err)
	}
	defer out.Close()

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")

	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encoding spdx sbom: %w", err)
	}

	return nil
}

func (sx *SPDX) imagePackage(opts *options.Options) (p *Package) {
	// Main package purl
	mmMain := map[string]string{}
	if opts.ImageInfo.Tag != "" {
		mmMain["tag"] = opts.ImageInfo.Tag
	}
	if opts.ImageInfo.Repository != "" {
		mmMain["repository_url"] = opts.ImageInfo.Repository
	}
	if opts.ImageInfo.Arch.String() != "" {
		mmMain["arch"] = opts.ImageInfo.Arch.ToOCIPlatform().Architecture
	}

	return &Package{
		ID: stringToIdentifier(fmt.Sprintf(
			"SPDXRef-Package-%s", opts.ImageInfo.ImageDigest,
		)),
		Name:             opts.ImageInfo.Name + "@" + opts.ImageInfo.ImageDigest,
		LicenseConcluded: NOASSERTION,
		LicenseDeclared:  NOASSERTION,
		DownloadLocation: NOASSERTION,
		CopyrightText:    NOASSERTION,
		FilesAnalyzed:    false,
		Description:      "apko container image",
		Checksums: []Checksum{
			{
				Algorithm: "SHA256",
				Value:     strings.TrimPrefix(opts.ImageInfo.ImageDigest, "sha256:"),
			},
		},
		ExternalRefs: []ExternalRef{
			{
				Category: "PACKAGE_MANAGER",
				Type:     "purl",
				Locator: purl.NewPackageURL(
					purl.TypeOCI, "", opts.ImageInfo.Name, opts.ImageInfo.ImageDigest,
					purl.QualifiersFromMap(mmMain), "",
				).String(),
			},
		},
	}
}

// apkPackage returns a SPDX package describing an apk
func (sx *SPDX) apkPackage(opts *options.Options, pkg *repository.Package) (p Package, err error) {
	p = Package{
		ID: stringToIdentifier(fmt.Sprintf(
			"SPDXRef-Package-%s-%s", pkg.Name, pkg.Version,
		)),
		Name:             pkg.Name,
		Version:          pkg.Version,
		FilesAnalyzed:    false,
		LicenseConcluded: pkg.License,
		LicenseDeclared:  NOASSERTION,
		Description:      pkg.Description,
		DownloadLocation: pkg.URL,
		Originator:       pkg.Maintainer,
		SourceInfo:       "Package info from apk database",
		CopyrightText:    NOASSERTION,
		Checksums: []Checksum{
			{
				Algorithm: "SHA1",
				Value:     fmt.Sprintf("%x", pkg.Checksum),
			},
		},
		ExternalRefs: []ExternalRef{
			{
				Category: "PACKAGE_MANAGER",
				Locator: purl.NewPackageURL(
					"apk", opts.OS.ID, pkg.Name, pkg.Version,
					purl.QualifiersFromMap(
						map[string]string{"arch": opts.ImageInfo.Arch.ToAPK()},
					), "").String(),
				Type: "purl",
			},
		},
	}
	return p, nil
}

// LayerPackage returns a package describing the layer
func (sx *SPDX) layerPackage(opts *options.Options) (p *Package, err error) {
	layerPackageName := opts.ImageInfo.LayerDigest
	if opts.ImageInfo.Name != "" {
		layerPackageName = opts.ImageInfo.Name + "@" + opts.ImageInfo.LayerDigest
	}

	if opts.ImageInfo.Reference != "" {
		x := ""
		if !strings.Contains(opts.ImageInfo.Reference, "/") {
			x = "index.docker.io/library/"
		}
		layerPackageName = fmt.Sprintf("SPDXRef-%s%s", x, opts.ImageInfo.Reference)
	}
	mainPkgID := stringToIdentifier(layerPackageName)

	// Main package purl
	mmMain := map[string]string{}
	if opts.ImageInfo.Tag != "" {
		mmMain["tag"] = opts.ImageInfo.Tag
	}
	if opts.ImageInfo.Repository != "" {
		mmMain["repository_url"] = opts.ImageInfo.Repository
	}
	if opts.ImageInfo.Arch.String() != "" {
		mmMain["arch"] = opts.ImageInfo.Arch.ToOCIPlatform().Architecture
	}

	layerPackage := Package{
		ID:               fmt.Sprintf("SPDXRef-Package-%s", mainPkgID),
		Name:             layerPackageName,
		Version:          opts.OS.Version,
		FilesAnalyzed:    false,
		LicenseConcluded: NOASSERTION,
		LicenseDeclared:  NOASSERTION,
		Description:      "apko operating system layer",
		DownloadLocation: NOASSERTION,
		Originator:       "",
		CopyrightText:    NOASSERTION,
		Checksums:        []Checksum{},
		ExternalRefs: []ExternalRef{
			{
				Category: "PACKAGE_MANAGER",
				Type:     "purl",
				Locator: purl.NewPackageURL(
					purl.TypeOCI, "", opts.ImageInfo.Name, opts.ImageInfo.LayerDigest,
					purl.QualifiersFromMap(mmMain), "",
				).String(),
			},
		},
	}
	return &layerPackage, nil
}

type Document struct {
	ID                string         `json:"SPDXID"`
	Name              string         `json:"name"`
	Version           string         `json:"spdxVersion"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	DataLicense       string         `json:"dataLicense"`
	Namespace         string         `json:"documentNamespace"`
	DocumentDescribes []string       `json:"documentDescribes"`
	Packages          []Package      `json:"packages"`
	Relationships     []Relationship `json:"relationships"`
}

type CreationInfo struct {
	Created            string   `json:"created"` // Date
	Creators           []string `json:"creators"`
	LicenseListVersion string   `json:"licenseListVersion"`
}

type Package struct {
	ID               string        `json:"SPDXID"`
	Name             string        `json:"name"`
	Version          string        `json:"versionInfo"`
	FilesAnalyzed    bool          `json:"filesAnalyzed"`
	LicenseConcluded string        `json:"licenseConcluded"`
	LicenseDeclared  string        `json:"licenseDeclared"`
	Description      string        `json:"description"`
	DownloadLocation string        `json:"downloadLocation"`
	Originator       string        `json:"originator"`
	SourceInfo       string        `json:"sourceInfo"`
	CopyrightText    string        `json:"copyrightText"`
	Checksums        []Checksum    `json:"checksums"`
	ExternalRefs     []ExternalRef `json:"externalRefs"`
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

type ExternalRef struct {
	Category string `json:"referenceCategory"`
	Locator  string `json:"referenceLocator"`
	Type     string `json:"referenceType"`
}

type Relationship struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}
