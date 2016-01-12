/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package registry

import (
	"github.com/kubernetes/deployment-manager/common"
	"github.com/kubernetes/deployment-manager/util"

	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// RegistryProvider is a factory for Registry instances.
type RegistryProvider interface {
	GetRegistryByShortURL(URL string) (Registry, error)
	GetRegistryByName(registryName string) (Registry, error)
}

// GithubRegistryProvider is a factory for GithubRegistry instances.
type GithubRegistryProvider interface {
	GetGithubRegistry(cr common.Registry) (GithubRegistry, error)
}

func NewDefaultRegistryProvider() RegistryProvider {
	return NewRegistryProvider(nil, nil)
}

func NewRegistryProvider(rs common.RegistryService, grp GithubRegistryProvider) RegistryProvider {
	if rs == nil {
		rs = NewInmemRegistryService()
	}

	registries := make(map[string]Registry)
	rp := &registryProvider{rs: rs, registries: registries}
	if grp == nil {
		grp = rp
	}

	rp.grp = grp
	return rp
}

type registryProvider struct {
	sync.RWMutex
	rs         common.RegistryService
	grp        GithubRegistryProvider
	registries map[string]Registry
}

func (rp registryProvider) GetRegistryByShortURL(URL string) (Registry, error) {
	rp.RLock()
	defer rp.RUnlock()

	result := rp.findRegistryByShortURL(URL)
	if result == nil {
		cr, err := rp.rs.GetByURL(URL)
		if err != nil {
			return nil, err
		}

		r, err := rp.grp.GetGithubRegistry(*cr)
		if err != nil {
			return nil, err
		}

		rp.registries[r.GetRegistryName()] = r
		result = r
	}

	return result, nil
}

// findRegistryByShortURL trims the scheme from both the supplied URL
// and the short URL returned by GetRegistryShortURL.
func (rp registryProvider) findRegistryByShortURL(URL string) Registry {
	trimmed := util.TrimURLScheme(URL)
	for _, r := range rp.registries {
		if strings.HasPrefix(trimmed, util.TrimURLScheme(r.GetRegistryShortURL())) {
			return r
		}
	}

	return nil
}

func (rp registryProvider) GetRegistryByName(registryName string) (Registry, error) {
	rp.RLock()
	defer rp.RUnlock()

	result, ok := rp.registries[registryName]
	if !ok {
		cr, err := rp.rs.Get(registryName)
		if err != nil {
			return nil, err
		}

		r, err := rp.grp.GetGithubRegistry(*cr)
		if err != nil {
			return nil, err
		}

		rp.registries[r.GetRegistryName()] = r
		result = r
	}

	return result, nil
}

func ParseRegistryFormat(rf common.RegistryFormat) map[common.RegistryFormat]bool {
	split := strings.Split(string(rf), ";")
	var result = map[common.RegistryFormat]bool{}
	for _, format := range split {
		result[common.RegistryFormat(format)] = true
	}

	return result
}

func (rp registryProvider) GetGithubRegistry(cr common.Registry) (GithubRegistry, error) {
	if cr.Type == common.GithubRegistryType {
		fMap := ParseRegistryFormat(cr.Format)
		if fMap[common.UnversionedRegistry] && fMap[common.OneLevelRegistry] {
			return NewGithubPackageRegistry(cr.Name, cr.URL, nil)
		}

		if fMap[common.VersionedRegistry] && fMap[common.CollectionRegistry] {
			return NewGithubTemplateRegistry(cr.Name, cr.URL, nil)
		}

		return nil, fmt.Errorf("unknown registry format: %s", cr.Format)
	}

	return nil, fmt.Errorf("unknown registry type: %s", cr.Type)
}

// RE for a registry type that does support versions and has collections.
var TemplateRegistryMatcher = regexp.MustCompile("github.com/(.*)/(.*)/(.*)/(.*):(.*)")

// RE for a registry type that does not support versions and does not have collections.
var PackageRegistryMatcher = regexp.MustCompile("github.com/(.*)/(.*)/(.*)")

// IsGithubShortType returns whether a given type is a type description in a short format to a github repository type.
// For now, this means using github types:
// github.com/owner/repo/qualifier/type:version
// for example:
// github.com/kubernetes/application-dm-templates/storage/redis:v1
func IsGithubShortType(t string) bool {
	return TemplateRegistryMatcher.MatchString(t)
}

// IsGithubShortPackageType returns whether a given type is a type description in a short format to a github
// package repository type.
// For now, this means using github types:
// github.com/owner/repo/type
// for example:
// github.com/helm/charts/cassandra
func IsGithubShortPackageType(t string) bool {
	return PackageRegistryMatcher.MatchString(t)
}

// GetDownloadURLs checks a type to see if it is either a short git hub url or a fully specified URL
// and returns the URLs that should be used to fetch it. If the url is not fetchable (primitive type
// for example), it returns an empty slice.
func GetDownloadURLs(rp RegistryProvider, t string) ([]string, error) {
	if IsGithubShortType(t) {
		return ShortTypeToDownloadURLs(rp, t)
	} else if IsGithubShortPackageType(t) {
		return ShortTypeToPackageDownloadURLs(rp, t)
	} else if util.IsHttpUrl(t) {
		result, err := url.Parse(t)
		if err != nil {
			return nil, fmt.Errorf("cannot parse download URL %s: %s", t, err)
		}

		return []string{result.String()}, nil
	}

	return []string{}, nil
}

// ShortTypeToDownloadURLs converts a github URL into downloadable URL from github.
// Input must be of the type and is assumed to have been validated before this call:
// github.com/owner/repo/qualifier/type:version
// for example:
// github.com/kubernetes/application-dm-templates/storage/redis:v1
func ShortTypeToDownloadURLs(rp RegistryProvider, t string) ([]string, error) {
	m := TemplateRegistryMatcher.FindStringSubmatch(t)
	if len(m) != 6 {
		return nil, fmt.Errorf("cannot parse short github url: %s", t)
	}

	r, err := rp.GetRegistryByShortURL(t)
	if err != nil {
		return nil, err
	}

	if r == nil {
		panic(fmt.Errorf("cannot get github registry for %s", t))
	}

	tt, err := NewType(m[3], m[4], m[5])
	if err != nil {
		return nil, err
	}

	urls, err := r.GetDownloadURLs(tt)
	if err != nil {
		return nil, err
	}

	return util.ConvertURLsToStrings(urls), err
}

// ShortTypeToPackageDownloadURLs converts a github URL into downloadable URLs from github.
// Input must be of the type and is assumed to have been validated before this call:
// github.com/owner/repo/type
// for example:
// github.com/helm/charts/cassandra
func ShortTypeToPackageDownloadURLs(rp RegistryProvider, t string) ([]string, error) {
	m := PackageRegistryMatcher.FindStringSubmatch(t)
	if len(m) != 4 {
		return nil, fmt.Errorf("Failed to parse short github url: %s", t)
	}

	r, err := rp.GetRegistryByShortURL(t)
	if err != nil {
		return nil, err
	}

	tt, err := NewType("", m[3], "")
	if err != nil {
		return nil, err
	}

	urls, err := r.GetDownloadURLs(tt)
	if err != nil {
		return nil, err
	}

	return util.ConvertURLsToStrings(urls), err
}
