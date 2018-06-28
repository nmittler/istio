//  Copyright 2018 Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package util

import "sync"

var (
	// IdentityURLInterceptor is a URLInterceptor that always returns the original url, unaltered.
	IdentityURLInterceptor = &identityURLInterceptor{}
)

// URLInterceptor is a simple interface that affords the testing infrastructure the ability to alter URLs before they are used for network I/O.
type URLInterceptor interface {
	// InterceptURL provides a point of interception, allowing the application to alter URLs before they are used for network I/O.
	InterceptURL(url string) (string, error)
}

type identityURLInterceptor struct{}

// InterceptURL implements the URLInterceptor interface.
func (i *identityURLInterceptor) InterceptURL(url string) (string, error) {
	return url, nil
}

// DelegatingURLInterceptor a URLInterceptor that delegates to another interceptor. If no delegate is set, defaults to using the IdentityURLInterceptor.
type DelegatingURLInterceptor struct {
	delegate URLInterceptor
	mutex    sync.Mutex
}

// InterceptURL implements the URLInterceptor interface.
func (i *DelegatingURLInterceptor) InterceptURL(url string) (string, error) {
	return i.getDelegate().InterceptURL(url)
}

// SetDelegate sets the delegate interceptor. If nil, the IdentityURLInterceptor will be used.
func (i *DelegatingURLInterceptor) SetDelegate(delegate URLInterceptor) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	i.delegate = delegate
}

func (i *DelegatingURLInterceptor) getDelegate() URLInterceptor {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.delegate != nil {
		return i.delegate
	}
	return IdentityURLInterceptor
}
